package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/minio/minio-go/v7"
)

type InboxDelivery struct {
	ID                string   `json:"id"`
	NotificationID    string   `json:"notification_id"`
	TaskID            *int     `json:"task_id,omitempty"`
	TaskPublicID      string   `json:"task_public_id,omitempty"`
	CampaignID        string   `json:"campaign_id"`
	UploadID          string   `json:"upload_id,omitempty"`
	Status            string   `json:"status"`
	FailureReason     string   `json:"failure_reason,omitempty"`
	RawDownloadURL    string   `json:"raw_download_url,omitempty"`
	GradedDownloadURL string   `json:"graded_download_url,omitempty"`
	FitsURL           string   `json:"fits_url,omitempty"`
	PointsEarned      *float64 `json:"points_earned,omitempty"`
	StackEligible     *bool    `json:"stack_eligible,omitempty"`
	TelescopeID       string   `json:"telescope_id,omitempty"`
	CreatedAt         string   `json:"created_at"`
}

func presignObjectURL(bucket, objectKey string, expiry time.Duration) (string, error) {
	if objectKey == "" {
		return "", nil
	}
	if bucket == "" {
		bucket = objectStoreBucket
	}
	mc, err := getObjectStorePresignClient()
	if err != nil {
		return "", err
	}
	u, err := mc.PresignedGetObject(context.Background(), bucket, objectKey, expiry, nil)
	if err != nil {
		return "", err
	}
	url := u.String()
	if err := assertDirectLandingURL(url); err != nil {
		return "", err
	}
	return url, nil
}

func handleInboxPoll(w http.ResponseWriter, r *http.Request) {
	requireResearcherJWT(func(w http.ResponseWriter, r *http.Request) {
		claims := claimsFromContext(r.Context())
		since := r.URL.Query().Get("since")
		campaignID := r.URL.Query().Get("campaign_id")

		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()

		query := `
			SELECT d.id::text, d.campaign_id::text, d.task_id, d.task_public_id::text, d.upload_id,
			       d.status, d.failure_reason, d.raw_object_key, d.graded_object_key, d.bucket,
			       d.points_earned, d.stack_eligible, d.created_at, fg.telescope_id
			FROM inbox_deliveries d
			LEFT JOIN frame_grades fg ON d.frame_grade_id = fg.id
			WHERE d.user_id = $1::uuid AND d.acked_at IS NULL
		`
		args := []any{claims.UserID}
		n := 2
		if since != "" {
			query += ` AND d.created_at > $` + strconv.Itoa(n)
			args = append(args, since)
			n++
		}
		if campaignID != "" {
			if err := assertCampaignOwner(ctx, campaignID, claims.UserID); err != nil {
				writeError(w, 403, "Forbidden")
				return
			}
			query += ` AND d.campaign_id = $` + strconv.Itoa(n) + `::uuid`
			args = append(args, campaignID)
		}
		query += ` ORDER BY d.created_at ASC LIMIT 200`

		rows, err := db.Query(ctx, query, args...)
		if err != nil {
			log.Printf("inbox poll query: %v", err)
			writeError(w, 500, "Database error")
			return
		}
		defer rows.Close()

		// Scan all rows first so we can close the DB result (and return the
		// pool connection) before R2 presign, which can stall on cold DNS/IPv6.
		var pending []inboxDeliveryRaw
		for rows.Next() {
			raw, err := scanInboxDeliveryRaw(rows)
			if err != nil {
				writeError(w, 500, "Database error")
				return
			}
			pending = append(pending, raw)
		}
		if err := rows.Err(); err != nil {
			log.Printf("inbox poll rows: %v", err)
			writeError(w, 500, "Database error")
			return
		}
		rows.Close()

		deliveries := make([]InboxDelivery, 0, len(pending))
		for _, raw := range pending {
			deliveries = append(deliveries, attachInboxDownloadURLs(raw.d, raw.rawKey, raw.gradedKey, raw.bucket))
		}
		writeJSON(w, 200, map[string]any{"deliveries": deliveries})
	})(w, r)
}

func handleInboxAck(w http.ResponseWriter, r *http.Request) {
	requireResearcherJWT(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		claims := claimsFromContext(r.Context())
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		var rawKey, gradedKey, bucket *string
		err := db.QueryRow(ctx, `
			SELECT raw_object_key, graded_object_key, bucket
			FROM inbox_deliveries
			WHERE id = $1::uuid AND user_id = $2::uuid AND acked_at IS NULL
		`, id, claims.UserID).Scan(&rawKey, &gradedKey, &bucket)
		if err != nil {
			if err == pgx.ErrNoRows {
				writeError(w, 404, "Delivery not found")
				return
			}
			writeError(w, 500, "Database error")
			return
		}

		tag, err := db.Exec(ctx, `
			UPDATE inbox_deliveries
			SET acked_at = NOW()
			WHERE id = $1::uuid AND user_id = $2::uuid AND acked_at IS NULL
		`, id, claims.UserID)
		if err != nil {
			writeError(w, 500, "Database error")
			return
		}
		if tag.RowsAffected() == 0 {
			writeError(w, 404, "Delivery not found")
			return
		}

		// Metadata-plane delete on R2 after researcher ack (#119 / #394).
		go deleteLandingObjectsAfterAck(bucket, rawKey, gradedKey)

		writeJSON(w, 200, map[string]any{"acknowledged": true, "id": id})
	})(w, r)
}

func deleteLandingObjectsAfterAck(bucket, rawKey, gradedKey *string) {
	mc, err := getObjectStoreClient()
	if err != nil {
		log.Printf("inbox ack: R2 unavailable for delete: %v", err)
		return
	}
	bkt := objectStoreBucket
	if bucket != nil && *bucket != "" {
		bkt = *bucket
	}
	keys := map[string]struct{}{}
	if rawKey != nil && *rawKey != "" {
		keys[*rawKey] = struct{}{}
	}
	if gradedKey != nil && *gradedKey != "" {
		keys[*gradedKey] = struct{}{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for key := range keys {
		if err := mc.RemoveObject(ctx, bkt, key, minio.RemoveObjectOptions{}); err != nil {
			log.Printf("inbox ack: RemoveObject bucket=%s key=%s: %v", bkt, key, err)
		}
	}
}

type inboxDeliveryRaw struct {
	d         InboxDelivery
	rawKey    *string
	gradedKey *string
	bucket    *string
}

func scanInboxDeliveryRaw(row pgx.Row) (inboxDeliveryRaw, error) {
	var raw inboxDeliveryRaw
	var taskPublicID *string
	var uploadID *string
	var failureReason *string
	var telescopeID *string
	var createdAt time.Time
	err := row.Scan(
		&raw.d.ID, &raw.d.CampaignID, &raw.d.TaskID, &taskPublicID, &uploadID,
		&raw.d.Status, &failureReason, &raw.rawKey, &raw.gradedKey, &raw.bucket,
		&raw.d.PointsEarned, &raw.d.StackEligible, &createdAt, &telescopeID,
	)
	if err != nil {
		log.Printf("inbox scan: %v", err)
		return raw, err
	}
	raw.d.NotificationID = raw.d.ID
	if taskPublicID != nil {
		raw.d.TaskPublicID = *taskPublicID
	}
	if uploadID != nil {
		raw.d.UploadID = *uploadID
	}
	if failureReason != nil {
		raw.d.FailureReason = *failureReason
	}
	if telescopeID != nil {
		raw.d.TelescopeID = *telescopeID
	}
	raw.d.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
	return raw, nil
}

func attachInboxDownloadURLs(d InboxDelivery, rawKey, gradedKey, bucket *string) InboxDelivery {
	bkt := ensureObjectStoreBucket()
	// Prefer live R2_BUCKET over stale delivery.bucket (legacy default was "saucepan").
	if bucket != nil && *bucket != "" && (os.Getenv("R2_BUCKET") == "" || *bucket == bkt) {
		bkt = *bucket
	}
	expiry := 15 * time.Minute
	if rawKey != nil && *rawKey != "" {
		if url, err := presignObjectURL(bkt, *rawKey, expiry); err == nil {
			d.RawDownloadURL = url
		}
	}
	if gradedKey != nil && *gradedKey != "" {
		if url, err := presignObjectURL(bkt, *gradedKey, expiry); err == nil {
			d.GradedDownloadURL = url
			d.FitsURL = url
		}
	} else if d.RawDownloadURL != "" {
		d.GradedDownloadURL = d.RawDownloadURL
		d.FitsURL = d.RawDownloadURL
	}
	return d
}

func assertCampaignOwner(ctx context.Context, campaignID, userID string) error {
	var owner *string
	err := db.QueryRow(ctx, `SELECT created_by::text FROM campaigns WHERE id = $1::uuid`, campaignID).Scan(&owner)
	if err != nil {
		return err
	}
	if owner == nil || *owner != userID {
		return pgx.ErrNoRows
	}
	return nil
}

func assertCampaignAccess(ctx context.Context, campaignID, userID string) error {
	return assertCampaignOwner(ctx, campaignID, userID)
}

func assertObservationGroupAccess(ctx context.Context, groupID, userID string) error {
	var campaignID string
	err := db.QueryRow(ctx, `SELECT campaign_id::text FROM observation_groups WHERE id = $1::uuid`, groupID).Scan(&campaignID)
	if err != nil {
		return err
	}
	if assertCampaignAccess(ctx, campaignID, userID) == nil {
		return nil
	}
	var allowed bool
	err = db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM observation_group_members ogm
			JOIN user_devices ud ON ud.telescope_id = ogm.telescope_id
			WHERE ogm.group_id = $1::uuid AND ud.user_id = $2::uuid
		)`, groupID, userID).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return pgx.ErrNoRows
	}
	return nil
}

func createInboxDeliveryFromGrade(
	ctx context.Context,
	frameGradeID int,
	taskPK *int,
	uploadID string,
	data map[string]any,
	pointsEarned float64,
	stackEligible bool,
) {
	if taskPK == nil {
		return
	}
	var campaignID, taskPublicID, ownerID *string
	err := db.QueryRow(ctx, `
		SELECT t.campaign_id::text, t.public_id::text, c.created_by::text
		FROM tasks t
		LEFT JOIN campaigns c ON t.campaign_id = c.id
		WHERE t.id = $1
	`, *taskPK).Scan(&campaignID, &taskPublicID, &ownerID)
	if err != nil || campaignID == nil || *campaignID == "" || ownerID == nil || *ownerID == "" {
		return
	}

	rawKey := stringField(data, "object_key")
	if rawKey == "" {
		if qm, ok := data["quality_metrics"].(map[string]any); ok {
			rawKey = stringField(qm, "object_key")
		}
	}
	gradedKey := stringField(data, "graded_object_key")
	if gradedKey == "" {
		gradedKey = rawKey
	}

	status := "completed"
	var failureReason *string
	if headline := intFromAny(data["headline"]); headline <= 0 && !stackEligible {
		status = "failed"
		msg := "frame not stack-eligible"
		failureReason = &msg
	}

	se := stackEligible
	bkt := ensureObjectStoreBucket()
	_, _ = db.Exec(ctx, `
		INSERT INTO inbox_deliveries (
			user_id, campaign_id, task_id, task_public_id, frame_grade_id, upload_id,
			status, failure_reason, raw_object_key, graded_object_key, bucket,
			points_earned, stack_eligible
		) VALUES ($1::uuid, $2::uuid, $3, $4::uuid, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	`, *ownerID, *campaignID, *taskPK, taskPublicID, frameGradeID, nullString(uploadID),
		status, failureReason, nullString(rawKey), nullString(gradedKey), bkt, pointsEarned, &se)
}

func stringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	switch s := v.(type) {
	case string:
		return s
	default:
		return ""
	}
}
