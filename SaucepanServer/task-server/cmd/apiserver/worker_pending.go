package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Durable pending pull jobs for the bridge worker (#170).
// Falls back to in-memory if the table is missing (unit tests / pre-migration).

type workerPendingJob struct {
	TaskID      int64    `json:"task_id"`
	CampaignID  int64    `json:"campaign_id"`
	TelescopeID string   `json:"telescope_id,omitempty"`
	ObjectKey   string   `json:"object_key,omitempty"`
	ObjectKeys  []string `json:"object_keys,omitempty"`
	ProductMode string   `json:"product_mode,omitempty"`
}

var (
	workerJobsMu sync.Mutex
	workerJobs   []workerPendingJob
)

func workerPendingMax() int {
	n := 1000
	if v := strings.TrimSpace(os.Getenv("WORKER_PENDING_MAX")); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			n = parsed
		}
	}
	return n
}

func enqueueWorkerJob(job workerPendingJob) error {
	if job.ProductMode == "" {
		job.ProductMode = "per_frame"
	}
	if job.ObjectKey == "" && len(job.ObjectKeys) > 0 {
		job.ObjectKey = job.ObjectKeys[0]
	}
	if job.ProductMode == "stack" && job.ObjectKey != "" && len(job.ObjectKeys) == 0 {
		job.ObjectKeys = []string{job.ObjectKey}
	}
	max := workerPendingMax()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if db != nil {
		if job.ProductMode == "stack" {
			tag, err := db.Exec(ctx, `
				UPDATE worker_pending_jobs
				SET object_keys = CASE
					WHEN object_keys @> jsonb_build_array($2::text) THEN object_keys
					ELSE object_keys || jsonb_build_array($2::text)
				END
				WHERE task_id = $1 AND product_mode = 'stack' AND status = 'pending'
			`, job.TaskID, job.ObjectKey)
			if err == nil && tag.RowsAffected() > 0 {
				return nil
			}
		}
		var pendingCount int64
		err := db.QueryRow(ctx, `
			SELECT COUNT(*) FROM worker_pending_jobs WHERE status = 'pending'
		`).Scan(&pendingCount)
		if err == nil && pendingCount >= int64(max) {
			log.Printf("worker pending db overflow count=%d max=%d", pendingCount, max)
			return errWorkerPendingFull
		}
		keysJSON, _ := json.Marshal(job.ObjectKeys)
		tag, err := db.Exec(ctx, `
			INSERT INTO worker_pending_jobs (task_id, campaign_id, telescope_id, object_key, object_keys, product_mode, status)
			VALUES ($1, $2, $3, $4, $5::jsonb, $6, 'pending')
			ON CONFLICT (object_key) WHERE status IN ('pending', 'leased') DO NOTHING
		`, job.TaskID, job.CampaignID, job.TelescopeID, job.ObjectKey, string(keysJSON), job.ProductMode)
		if err != nil {
			log.Printf("enqueueWorkerJob db: %v — falling back to memory", err)
		} else {
			if tag.RowsAffected() == 0 {
				return nil
			}
			return nil
		}
	}
	workerJobsMu.Lock()
	defer workerJobsMu.Unlock()
	if job.ProductMode == "stack" {
		for i := range workerJobs {
			if workerJobs[i].TaskID != job.TaskID || workerJobs[i].ProductMode != "stack" {
				continue
			}
			for _, key := range job.ObjectKeys {
				seen := false
				for _, existing := range workerJobs[i].ObjectKeys {
					if existing == key {
						seen = true
						break
					}
				}
				if !seen {
					workerJobs[i].ObjectKeys = append(workerJobs[i].ObjectKeys, key)
				}
			}
			return nil
		}
	}
	if len(workerJobs) >= max {
		log.Printf("worker pending memory overflow len=%d max=%d", len(workerJobs), max)
		return errWorkerPendingFull
	}
	workerJobs = append(workerJobs, job)
	return nil
}

var errWorkerPendingFull = errors.New("worker pending queue full")

func claimWorkerJobs(ctx context.Context, limit int) ([]workerPendingJob, error) {
	if db == nil {
		return nil, errors.New("database not configured")
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, `
		SELECT id::text, task_id, campaign_id, telescope_id, object_key, object_keys, product_mode
		FROM worker_pending_jobs
		WHERE status = 'pending'
		  AND (product_mode <> 'stack' OR jsonb_array_length(object_keys) >= 2)
		ORDER BY created_at ASC
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type row struct {
		id          string
		taskID      int64
		campaignID  int64
		telescopeID string
		objectKey   string
		keysRaw     []byte
		productMode string
	}
	var claimed []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.taskID, &r.campaignID, &r.telescopeID, &r.objectKey, &r.keysRaw, &r.productMode); err != nil {
			return nil, err
		}
		claimed = append(claimed, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]workerPendingJob, 0, len(claimed))
	for _, r := range claimed {
		_, err := tx.Exec(ctx, `
			UPDATE worker_pending_jobs
			SET status = 'done', leased_at = NOW(), completed_at = NOW()
			WHERE id = $1::uuid AND status = 'pending'
		`, r.id)
		if err != nil {
			return nil, err
		}
		job := workerPendingJob{
			TaskID:      r.taskID,
			CampaignID:  r.campaignID,
			TelescopeID: r.telescopeID,
			ObjectKey:   r.objectKey,
			ProductMode: r.productMode,
		}
		if len(r.keysRaw) > 0 && string(r.keysRaw) != "null" {
			_ = json.Unmarshal(r.keysRaw, &job.ObjectKeys)
		}
		out = append(out, job)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// requireWorkerAuth fails closed when WORKER_TOKEN is unset (except DEV_MODE=1). Refs #254.
func requireWorkerAuth(w http.ResponseWriter, r *http.Request) bool {
	tok := strings.TrimSpace(os.Getenv("WORKER_TOKEN"))
	if tok == "" {
		if os.Getenv("DEV_MODE") == "1" {
			log.Println("WARNING: WORKER_TOKEN unset — worker routes open only because DEV_MODE=1")
			return true
		}
		writeError(w, 503, "Worker API unavailable: WORKER_TOKEN not configured")
		return false
	}
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") || !secretEqual(auth[7:], tok) {
		writeError(w, 401, "Unauthorized")
		return false
	}
	return true
}

// assertWorkerTokenConfigured refuses to boot production without WORKER_TOKEN (#254).
func assertWorkerTokenConfigured() error {
	if strings.TrimSpace(os.Getenv("WORKER_TOKEN")) != "" {
		return nil
	}
	if os.Getenv("DEV_MODE") == "1" {
		log.Println("WARNING: WORKER_TOKEN unset (DEV_MODE=1) — worker queue is open")
		return nil
	}
	return errors.New("WORKER_TOKEN is required when DEV_MODE is not 1 (set token or DEV_MODE=1 for local)")
}

// GET /api/v1/worker/pending — pull jobs for local/physical bridge worker.
func handleWorkerPending(w http.ResponseWriter, r *http.Request) {
	if !requireWorkerAuth(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	jobs, err := claimWorkerJobs(ctx, 100)
	if err != nil {
		log.Printf("worker pending claim: %v — using memory fallback", err)
		jobs = nil
	}
	workerJobsMu.Lock()
	mem := workerJobs
	workerJobs = nil
	workerJobsMu.Unlock()
	if jobs == nil {
		jobs = []workerPendingJob{}
	}
	if len(mem) > 0 {
		jobs = append(jobs, mem...)
	}
	writeJSON(w, 200, map[string]any{"jobs": jobs})
}

// POST /api/v1/worker/enqueue — hot path / complete hook can enqueue (dev + internal).
func handleWorkerEnqueue(w http.ResponseWriter, r *http.Request) {
	if !requireWorkerAuth(w, r) {
		return
	}
	var job workerPendingJob
	if err := decodeJSON(r, &job); err != nil {
		writeError(w, 400, "Invalid JSON")
		return
	}
	if job.TaskID == 0 || (job.ObjectKey == "" && len(job.ObjectKeys) == 0) {
		writeError(w, 400, "task_id and object_key(s) required")
		return
	}
	if err := enqueueWorkerJob(job); err != nil {
		if errors.Is(err, errWorkerPendingFull) {
			writeError(w, 503, "Worker pending queue full")
			return
		}
		writeError(w, 500, "Enqueue failed")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
