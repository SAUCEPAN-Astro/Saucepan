package main

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

// handleWorkerStackProduct turns a completed reference stack into the same
// researcher inbox delivery used for individual graded frames. The worker is
// the only caller; it already owns the object-store write and authenticates
// with WORKER_TOKEN.
func handleWorkerStackProduct(w http.ResponseWriter, r *http.Request) {
	if !requireWorkerAuth(w, r) {
		return
	}
	var req struct {
		TaskID    int64  `json:"task_id"`
		ObjectKey string `json:"object_key"`
		Bucket    string `json:"bucket"`
	}
	if err := decodeJSON(r, &req); err != nil || req.TaskID <= 0 || req.ObjectKey == "" {
		writeError(w, http.StatusBadRequest, "task_id and object_key are required")
		return
	}
	if req.Bucket == "" {
		req.Bucket = objectStoreBucket
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var campaignID, taskPublicID string
	var userID *string
	err := db.QueryRow(ctx, `
		SELECT t.campaign_id::text, t.public_id::text, c.created_by::text
		FROM tasks t JOIN campaigns c ON c.id = t.campaign_id
		WHERE t.id = $1
	`, req.TaskID).Scan(&campaignID, &taskPublicID, &userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "task or campaign not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	if userID == nil || *userID == "" {
		writeError(w, http.StatusConflict, "campaign has no owner")
		return
	}
	cleaned, err := canonicalizeLandingObjectKey(req.ObjectKey)
	if err != nil || !objectKeyUnderLandingPrefix(cleaned, campaignID, int(req.TaskID)) {
		writeError(w, http.StatusBadRequest, "object key is outside the task landing prefix")
		return
	}

	var deliveryID string
	err = db.QueryRow(ctx, `
		SELECT id::text FROM inbox_deliveries
		WHERE task_id = $1 AND graded_object_key = $2 AND status = 'completed'
		LIMIT 1
	`, req.TaskID, cleaned).Scan(&deliveryID)
	if err == nil {
		writeJSON(w, http.StatusOK, map[string]any{"delivery_id": deliveryID, "duplicate": true})
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}

	err = db.QueryRow(ctx, `
		INSERT INTO inbox_deliveries (
			user_id, campaign_id, task_id, task_public_id, status,
			raw_object_key, graded_object_key, bucket, points_earned, stack_eligible
		) VALUES ($1::uuid, $2::uuid, $3, $4::uuid, 'completed', $5, $5, $6, 0, true)
		RETURNING id::text
	`, *userID, campaignID, req.TaskID, taskPublicID, cleaned, req.Bucket).Scan(&deliveryID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create stack delivery")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"delivery_id": deliveryID, "duplicate": false})
}
