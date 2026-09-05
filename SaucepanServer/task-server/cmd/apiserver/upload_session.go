package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const defaultUploadSessionTTL = 6 * time.Hour

var (
	errUploadSessionNotFound   = errors.New("upload session not found")
	errUploadSessionForbidden  = errors.New("upload session forbidden")
	errUploadSessionExpired    = errors.New("upload session expired")
	errUploadDeviceRequired    = errors.New("device authentication required")
	errUploadTelescopeMismatch = errors.New("telescope_id does not match authenticated device")
)

type uploadDevice struct {
	NodeID      string
	UserID      string
	TelescopeID string
}

const uploadDeviceCtxKey contextKey = "uploadDevice"

func uploadDeviceFromContext(ctx context.Context) *uploadDevice {
	d, _ := ctx.Value(uploadDeviceCtxKey).(*uploadDevice)
	return d
}

func uploadSessionTTL() time.Duration {
	if raw := os.Getenv("UPLOAD_SESSION_TTL"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			return d
		}
	}
	return defaultUploadSessionTTL
}

type persistedUploadSession struct {
	SessionID   string
	S3UploadID  string
	NodeID      string
	TelescopeID string
	ObjectPath  string
	Bucket      string
	Grade       uploadGradeMeta
	ExpiresAt   time.Time
	CompletedAt *time.Time
}

func lookupDeviceByToken(ctx context.Context, token string) (*uploadDevice, error) {
	if db == nil {
		return nil, errors.New("database unavailable")
	}
	hash := hashDeviceToken(token)
	var device uploadDevice
	var telescopeID *string
	err := db.QueryRow(ctx, `
		SELECT node_id, user_id::text, telescope_id
		FROM user_devices
		WHERE device_token_hash = $1
	`, hash).Scan(&device.NodeID, &device.UserID, &telescopeID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errors.New("invalid device token")
		}
		return nil, err
	}
	if telescopeID != nil {
		device.TelescopeID = *telescopeID
	}
	return &device, nil
}

func requireUploadDevice(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := extractBearerToken(r)
		if token == "" {
			writeError(w, 401, "Missing Authorization header")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		device, err := lookupDeviceByToken(ctx, token)
		if err != nil {
			writeError(w, 401, "Invalid or expired device token")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), uploadDeviceCtxKey, device)))
	}
}

func resolveUploadTelescopeID(device *uploadDevice, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if device.TelescopeID != "" {
		if requested != "" && requested != device.TelescopeID {
			return "", errUploadTelescopeMismatch
		}
		return device.TelescopeID, nil
	}
	if requested == "" {
		return "", errors.New("telescope_id is required")
	}
	return requested, nil
}

func authorizeUploadSession(session *persistedUploadSession, device *uploadDevice) error {
	if session == nil || device == nil {
		return errUploadSessionNotFound
	}
	if session.CompletedAt != nil {
		return errUploadSessionNotFound
	}
	if time.Now().After(session.ExpiresAt) {
		return errUploadSessionExpired
	}
	if session.NodeID != device.NodeID {
		return errUploadSessionForbidden
	}
	if session.TelescopeID != device.TelescopeID {
		return errUploadSessionForbidden
	}
	return nil
}

func createUploadSession(ctx context.Context, device *uploadDevice, s3UploadID, objectPath, bucket string, grade uploadGradeMeta) (string, error) {
	if db == nil {
		return "", errors.New("database unavailable")
	}
	if device == nil {
		return "", errUploadDeviceRequired
	}
	gradeJSON, err := json.Marshal(grade)
	if err != nil {
		return "", fmt.Errorf("marshal grade meta: %w", err)
	}
	expiresAt := time.Now().Add(uploadSessionTTL())
	var sessionID string
	err = db.QueryRow(ctx, `
		INSERT INTO upload_sessions (
			s3_upload_id, node_id, telescope_id, object_path, bucket, grade_meta, expires_at
		) VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7)
		RETURNING session_id::text
	`, s3UploadID, device.NodeID, device.TelescopeID, objectPath, bucket, gradeJSON, expiresAt).Scan(&sessionID)
	if err != nil {
		return "", fmt.Errorf("insert upload session: %w", err)
	}
	return sessionID, nil
}

func getUploadSession(ctx context.Context, sessionID string) (*persistedUploadSession, error) {
	if db == nil {
		return nil, errors.New("database unavailable")
	}
	var session persistedUploadSession
	var gradeJSON []byte
	err := db.QueryRow(ctx, `
		SELECT session_id::text, s3_upload_id, node_id, telescope_id, object_path, bucket,
		       grade_meta, expires_at, completed_at
		FROM upload_sessions
		WHERE session_id = $1::uuid
	`, sessionID).Scan(
		&session.SessionID,
		&session.S3UploadID,
		&session.NodeID,
		&session.TelescopeID,
		&session.ObjectPath,
		&session.Bucket,
		&gradeJSON,
		&session.ExpiresAt,
		&session.CompletedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errUploadSessionNotFound
		}
		return nil, err
	}
	if len(gradeJSON) > 0 {
		_ = json.Unmarshal(gradeJSON, &session.Grade)
	}
	return &session, nil
}

func markUploadSessionComplete(ctx context.Context, sessionID string) error {
	if db == nil {
		return errors.New("database unavailable")
	}
	tag, err := db.Exec(ctx, `
		UPDATE upload_sessions
		SET completed_at = NOW()
		WHERE session_id = $1::uuid AND completed_at IS NULL
	`, sessionID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errUploadSessionNotFound
	}
	return nil
}

func deleteUploadSession(ctx context.Context, sessionID string) error {
	if db == nil {
		return errors.New("database unavailable")
	}
	_, err := db.Exec(ctx, `DELETE FROM upload_sessions WHERE session_id = $1::uuid`, sessionID)
	return err
}

func listExpiredUploadSessions(ctx context.Context, limit int) ([]persistedUploadSession, error) {
	if db == nil {
		return nil, errors.New("database unavailable")
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := db.Query(ctx, `
		SELECT session_id::text, s3_upload_id, node_id, telescope_id, object_path, bucket,
		       grade_meta, expires_at, completed_at
		FROM upload_sessions
		WHERE completed_at IS NULL AND expires_at < NOW()
		ORDER BY expires_at ASC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []persistedUploadSession
	for rows.Next() {
		var session persistedUploadSession
		var gradeJSON []byte
		if err := rows.Scan(
			&session.SessionID,
			&session.S3UploadID,
			&session.NodeID,
			&session.TelescopeID,
			&session.ObjectPath,
			&session.Bucket,
			&gradeJSON,
			&session.ExpiresAt,
			&session.CompletedAt,
		); err != nil {
			return nil, err
		}
		if len(gradeJSON) > 0 {
			_ = json.Unmarshal(gradeJSON, &session.Grade)
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

func sweepExpiredUploadSessions(ctx context.Context) {
	sessions, err := listExpiredUploadSessions(ctx, 50)
	if err != nil {
		log.Printf("upload session sweep: list expired: %v", err)
		return
	}
	if len(sessions) == 0 {
		return
	}

	mc, err := getObjectStoreClient()
	if err != nil {
		log.Printf("upload session sweep: R2 unavailable: %v", err)
		mc = nil
	}

	for _, session := range sessions {
		if mc != nil {
			if err := abortMultipartUpload(mc, session.Bucket, session.ObjectPath, session.S3UploadID); err != nil {
				log.Printf("upload session sweep: abort multipart session=%s: %v", session.SessionID, err)
			}
		}
		if err := deleteUploadSession(ctx, session.SessionID); err != nil {
			log.Printf("upload session sweep: delete session=%s: %v", session.SessionID, err)
		}
	}
	log.Printf("upload session sweep: cleaned %d expired session(s)", len(sessions))
}

func startUploadSessionSweeper() {
	if db == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			sweepExpiredUploadSessions(ctx)
			cancel()
		}
	}()
}

func writeUploadSessionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errUploadSessionNotFound):
		writeError(w, 404, "Upload not found")
	case errors.Is(err, errUploadSessionForbidden):
		writeError(w, 403, "Upload session not owned by this device")
	case errors.Is(err, errUploadSessionExpired):
		writeError(w, 410, "Upload session expired")
	default:
		writeError(w, 500, "Upload session error")
	}
}
