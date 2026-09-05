package main

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Trust model (grades ingest object keys — #257):
//
// GRADES_INGEST_TOKEN authenticates the compute/datalake caller but must NOT
// authorize arbitrary object-store reads via inbox/developer presign.
// Non-empty object_key / graded_object_key must sit under the task landing
// prefix {campaign_id}/{task_id}/ (canonicalized; ".." rejected).
//
// upload_sessions (#266) and worker_pending_jobs are consulted when present to
// reject keys already bound to a different task/telescope. They are not a full
// durable upload registry (sessions are deleted on complete), so absence of a
// registry row is allowed when the prefix check passes. Remaining work: durable
// object_key → (task_id, telescope_id) registry + optional HMAC attestation.

var (
	errObjectKeyEmpty      = errors.New("object key is empty")
	errObjectKeyAbsolute   = errors.New("object key must be relative")
	errObjectKeyDotDot     = errors.New("object key must not contain '..'")
	errObjectKeyOutside    = errors.New("object key outside landing prefix")
	errObjectKeyNoTask     = errors.New("object key requires a valid task_id")
	errObjectKeyForeignReg = errors.New("object key registered to another task or telescope")
)

// canonicalizeLandingObjectKey normalizes an object-store key for prefix checks.
func canonicalizeLandingObjectKey(key string) (string, error) {
	k := strings.ReplaceAll(strings.TrimSpace(key), "\\", "/")
	if k == "" {
		return "", errObjectKeyEmpty
	}
	if strings.HasPrefix(k, "/") {
		return "", errObjectKeyAbsolute
	}
	for _, seg := range strings.Split(k, "/") {
		if seg == ".." {
			return "", errObjectKeyDotDot
		}
	}
	cleaned := path.Clean(k)
	if cleaned == "." || cleaned == "" {
		return "", errObjectKeyEmpty
	}
	if strings.HasPrefix(cleaned, "../") || cleaned == ".." || strings.HasPrefix(cleaned, "/") {
		return "", errObjectKeyDotDot
	}
	return cleaned, nil
}

func isObjectStoreKey(key string) bool {
	k := strings.TrimSpace(key)
	if k == "" {
		return false
	}
	// Absolute FS paths and URLs are not R2 landing keys (staged_path may be local).
	if strings.HasPrefix(k, "/") || strings.Contains(k, "://") {
		return false
	}
	return true
}

func landingPrefix(campaignID string, taskID int) string {
	return fmt.Sprintf("%s/%d/", campaignID, taskID)
}

// objectKeyUnderLandingPrefix reports whether cleaned is under the task landing tree.
// Prefer {campaign_id}/{task_id}/ when campaignID is known (UUID from tasks).
// Also accept {any}/{task_id}/… because upload.go currently embeds numeric campaign
// ids that may not equal campaigns.id (UUID).
func objectKeyUnderLandingPrefix(cleaned, campaignID string, taskID int) bool {
	if campaignID != "" && strings.HasPrefix(cleaned, landingPrefix(campaignID, taskID)) {
		return true
	}
	parts := strings.Split(cleaned, "/")
	if len(parts) < 3 {
		return false
	}
	if parts[0] == "" || parts[0] == "." || parts[0] == ".." {
		return false
	}
	return parts[1] == strconv.Itoa(taskID)
}

func collectGradeObjectKeys(data map[string]any) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(v string) {
		if !isObjectStoreKey(v) {
			return
		}
		if _, ok := seen[v]; ok {
			return
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	add(stringField(data, "object_key"))
	add(stringField(data, "graded_object_key"))
	if qm, ok := data["quality_metrics"].(map[string]any); ok {
		add(stringField(qm, "object_key"))
	}
	return out
}

func validateGradeIngestObjectKeys(ctx context.Context, data map[string]any, taskPK *int, telescopeID string) error {
	keys := collectGradeObjectKeys(data)
	if len(keys) == 0 {
		return nil
	}
	if taskPK == nil {
		return errObjectKeyNoTask
	}

	var campaignID *string
	if db != nil {
		_ = db.QueryRow(ctx, `SELECT campaign_id::text FROM tasks WHERE id = $1`, *taskPK).Scan(&campaignID)
	}
	camp := ""
	if campaignID != nil {
		camp = *campaignID
	}

	for _, key := range keys {
		cleaned, err := canonicalizeLandingObjectKey(key)
		if err != nil {
			return fmt.Errorf("%w (%v)", errObjectKeyOutside, err)
		}
		if !objectKeyUnderLandingPrefix(cleaned, camp, *taskPK) {
			return fmt.Errorf("%w for task %d", errObjectKeyOutside, *taskPK)
		}
		if err := assertObjectKeyNotForeignRegistered(ctx, cleaned, *taskPK, telescopeID); err != nil {
			return err
		}
	}
	return nil
}

// assertObjectKeyNotForeignRegistered rejects keys already bound to another
// task (worker_pending_jobs) or telescope (upload_sessions). Missing tables or
// no matching row is OK — prefix check remains authoritative until a durable
// registry lands.
func assertObjectKeyNotForeignRegistered(ctx context.Context, key string, taskID int, telescopeID string) error {
	if db == nil {
		return nil
	}

	var otherTask int64
	err := db.QueryRow(ctx, `
		SELECT task_id FROM worker_pending_jobs
		WHERE object_key = $1 AND task_id <> $2
		LIMIT 1
	`, key, taskID).Scan(&otherTask)
	if err == nil {
		return errObjectKeyForeignReg
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) && !isUndefinedRelation(err) {
		// Soft-fail probe: do not block ingest on unexpected registry errors.
		return nil
	}

	if telescopeID != "" {
		var otherTel string
		err = db.QueryRow(ctx, `
			SELECT telescope_id FROM upload_sessions
			WHERE object_path = $1 AND telescope_id <> $2
			LIMIT 1
		`, key, telescopeID).Scan(&otherTel)
		if err == nil {
			return errObjectKeyForeignReg
		}
	}
	return nil
}

func isUndefinedRelation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "42P01"
	}
	return false
}
