package main

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

// Developer delivery inbox, quota, profile, and the inbox dispatch
// shims shared with the researcher surface. Split from developer.go (#431).

func handleDeveloperInboxPoll(w http.ResponseWriter, r *http.Request) {
	auth := apiKeyFromContext(r.Context())
	includeAll := r.URL.Query().Get("all") == "true"
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	query := `
		SELECT id, task_id, status, failure_reason, graded_object_key, bucket, original_spec, created_at
		FROM developer_deliveries
		WHERE user_id = $1::uuid
	`
	if !includeAll {
		query += ` AND acked_at IS NULL`
	}
	query += ` ORDER BY created_at ASC LIMIT 200`

	rows, err := db.Query(ctx, query, auth.UserID)
	if err != nil {
		writeError(w, 500, "Database error")
		return
	}
	defer rows.Close()

	deliveries := []map[string]any{}
	for rows.Next() {
		d, err := scanDeveloperDelivery(rows)
		if err != nil {
			writeError(w, 500, "Database error")
			return
		}
		deliveries = append(deliveries, d)
	}
	writeJSON(w, 200, map[string]any{"deliveries": deliveries})
}

func handleDeveloperInboxAck(w http.ResponseWriter, r *http.Request) {
	auth := apiKeyFromContext(r.Context())
	id := r.PathValue("notification_id")
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	tag, err := db.Exec(ctx, `
		UPDATE developer_deliveries SET acked_at = NOW()
		WHERE id = $1::int AND user_id = $2::uuid AND acked_at IS NULL
	`, id, auth.UserID)
	if err != nil {
		writeError(w, 500, "Database error")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, 404, "Notification not found")
		return
	}
	writeJSON(w, 200, map[string]any{"acknowledged": true})
}

func scanDeveloperDelivery(row pgx.Row) (map[string]any, error) {
	var id, taskID int
	var status string
	var failure *string
	var objectKey, bucket *string
	var specJSON []byte
	var createdAt time.Time
	if err := row.Scan(&id, &taskID, &status, &failure, &objectKey, &bucket, &specJSON, &createdAt); err != nil {
		return nil, err
	}
	var spec map[string]any
	_ = json.Unmarshal(specJSON, &spec)
	if spec == nil {
		spec = map[string]any{}
	}
	out := map[string]any{
		"notification_id": id,
		"task_id":         taskID,
		"status":          status,
		"original_spec":   spec,
		"acknowledged":    false,
		"created_at":      createdAt.UTC().Format(time.RFC3339),
	}
	if failure != nil {
		out["failure_reason"] = *failure
	}
	bkt := objectStoreBucket
	if bucket != nil && *bucket != "" {
		bkt = *bucket
	}
	if objectKey != nil && *objectKey != "" {
		if url, err := presignObjectURL(bkt, *objectKey, 15*time.Minute); err == nil {
			out["fits_url"] = url
		}
	}
	return out, nil
}

func createDeveloperDeliveryFromGrade(
	ctx context.Context,
	frameGradeID int,
	taskPK *int,
	uploadID string,
	data map[string]any,
	stackEligible bool,
) {
	if taskPK == nil {
		return
	}
	var devUserID *string
	var specJSON []byte
	err := db.QueryRow(ctx, `
		SELECT developer_user_id::text, COALESCE(original_spec, '{}'::jsonb)
		FROM tasks WHERE id = $1
	`, *taskPK).Scan(&devUserID, &specJSON)
	if err != nil || devUserID == nil || *devUserID == "" {
		return
	}

	gradedKey := stringField(data, "graded_object_key")
	if gradedKey == "" {
		gradedKey = stringField(data, "object_key")
	}
	status := "completed"
	var failureReason *string
	if headline := intFromAny(data["headline"]); headline <= 0 && !stackEligible {
		status = "failed"
		msg := "frame not stack-eligible"
		failureReason = &msg
	}

	_, _ = db.Exec(ctx, `
		INSERT INTO developer_deliveries (
			user_id, task_id, status, failure_reason, graded_object_key,
			original_spec, frame_grade_id, upload_id
		) VALUES ($1::uuid, $2, $3, $4, $5, $6::jsonb, $7, $8)
	`, *devUserID, *taskPK, status, failureReason, nullString(gradedKey),
		string(specJSON), frameGradeID, nullString(uploadID))
}

func handleDeveloperQuota(w http.ResponseWriter, r *http.Request) {
	auth := apiKeyFromContext(r.Context())
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var used int
	_ = db.QueryRow(ctx, `
		SELECT COUNT(*)::int FROM tasks WHERE developer_user_id = $1::uuid
	`, auth.UserID).Scan(&used)
	remaining := developerQuotaTotal - used
	if remaining < 0 {
		remaining = 0
	}
	writeJSON(w, 200, map[string]any{
		"quota_total":           developerQuotaTotal,
		"quota_used":            used,
		"quota_remaining":       remaining,
		"rate_limit_per_minute": 60,
		"rate_limit_per_hour":   500,
		"rate_limit_per_day":    5000,
	})
}

func handleDeveloperMe(w http.ResponseWriter, r *http.Request) {
	claims, ok := mustClaims(w, r, "authentication required")
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var email *string
	var username string
	var verified, approved bool
	err := db.QueryRow(ctx, `
		SELECT username, email, email_verified, researcher_approved FROM users WHERE id = $1
	`, claims.UserID).Scan(&username, &email, &verified, &approved)
	if err != nil {
		writeError(w, 404, "User not found")
		return
	}
	emailOut := ""
	if email != nil {
		emailOut = *email
	}
	writeJSON(w, 200, map[string]any{
		"username":            username,
		"email":               emailOut,
		"email_verified":      verified,
		"admin_approved":      approved, // retained key name
		"researcher_approved": approved, // #470: canonical name the SDK checks to show "awaiting approval"
		"is_active":           true,
	})
}

func dispatchInboxPoll(w http.ResponseWriter, r *http.Request) {
	if apiKeyFromRequest(r) != "" {
		requireAPIKey(devScopeStatusRead)(handleDeveloperInboxPoll)(w, r)
		return
	}
	handleInboxPoll(w, r)
}

func dispatchInboxAck(w http.ResponseWriter, r *http.Request) {
	if apiKeyFromRequest(r) != "" {
		id := r.PathValue("id")
		if id == "" {
			id = r.PathValue("notification_id")
		}
		r.SetPathValue("notification_id", id)
		requireAPIKey(devScopeStatusRead)(handleDeveloperInboxAck)(w, r)
		return
	}
	handleInboxAck(w, r)
}
