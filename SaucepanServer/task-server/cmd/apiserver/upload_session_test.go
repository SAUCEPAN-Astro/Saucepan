package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func setupUploadSessionTestDB(t *testing.T) {
	t.Helper()
	dsn := os.Getenv("TEST_PG_DSN")
	if dsn == "" {
		dsn = "postgres://postgres:password@localhost:5432/server_db?sslmode=disable"
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("postgres ping failed: %v", err)
	}

	schema := `
		CREATE TABLE IF NOT EXISTS users (
			id UUID PRIMARY KEY,
			username TEXT,
			password_hash TEXT NOT NULL DEFAULT '',
			email_verified BOOLEAN NOT NULL DEFAULT true,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE TABLE IF NOT EXISTS user_devices (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			node_id TEXT UNIQUE NOT NULL,
			device_token_hash TEXT NOT NULL,
			telescope_id TEXT,
			label TEXT,
			last_seen_at TIMESTAMPTZ,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE TABLE IF NOT EXISTS upload_sessions (
			session_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			s3_upload_id TEXT NOT NULL,
			node_id TEXT NOT NULL,
			telescope_id TEXT NOT NULL,
			object_path TEXT NOT NULL,
			bucket TEXT NOT NULL DEFAULT 'saucepan',
			grade_meta JSONB NOT NULL DEFAULT '{}',
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			expires_at TIMESTAMPTZ NOT NULL,
			completed_at TIMESTAMPTZ
		);
		CREATE UNIQUE INDEX IF NOT EXISTS ux_upload_sessions_s3_upload_id ON upload_sessions (s3_upload_id);
	`
	if _, err := pool.Exec(ctx, schema); err != nil {
		t.Fatalf("schema setup: %v", err)
	}
	_, _ = pool.Exec(ctx, `DELETE FROM upload_sessions`)
	_, _ = pool.Exec(ctx, `DELETE FROM user_devices WHERE node_id LIKE 'upload-test-%'`)
	_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id IN (
		'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa'::uuid,
		'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb'::uuid
	)`)

	db = pool
	t.Cleanup(func() {
		pool.Close()
		db = nil
	})
}

func seedUploadTestDevice(t *testing.T, userID, nodeID, telescopeID, deviceToken string) {
	t.Helper()
	ctx := context.Background()
	_, err := db.Exec(ctx, `
		INSERT INTO users (id, username, password_hash, email_verified)
		VALUES ($1::uuid, $2, 'hash', true)
		ON CONFLICT (id) DO NOTHING
	`, userID, nodeID+"-user")
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	_, err = db.Exec(ctx, `
		INSERT INTO user_devices (user_id, node_id, device_token_hash, telescope_id)
		VALUES ($1::uuid, $2, $3, $4)
	`, userID, nodeID, hashDeviceToken(deviceToken), telescopeID)
	if err != nil {
		t.Fatalf("seed device: %v", err)
	}
}

func TestAuthorizeUploadSessionRejectsCrossDevice(t *testing.T) {
	owner := &uploadDevice{NodeID: "node-a", TelescopeID: "tel-a"}
	attacker := &uploadDevice{NodeID: "node-b", TelescopeID: "tel-b"}
	session := &persistedUploadSession{
		NodeID:      "node-a",
		TelescopeID: "tel-a",
		ExpiresAt:   time.Now().Add(time.Hour),
	}
	if err := authorizeUploadSession(session, owner); err != nil {
		t.Fatalf("owner should be authorized: %v", err)
	}
	if err := authorizeUploadSession(session, attacker); !errors.Is(err, errUploadSessionForbidden) {
		t.Fatalf("expected forbidden, got %v", err)
	}
}

func TestAuthorizeUploadSessionRejectsExpired(t *testing.T) {
	device := &uploadDevice{NodeID: "node-a", TelescopeID: "tel-a"}
	session := &persistedUploadSession{
		NodeID:      "node-a",
		TelescopeID: "tel-a",
		ExpiresAt:   time.Now().Add(-time.Minute),
	}
	if err := authorizeUploadSession(session, device); err != errUploadSessionExpired {
		t.Fatalf("expected expired, got %v", err)
	}
}

func TestUploadSessionPersistedAcrossLookupPaths(t *testing.T) {
	setupUploadSessionTestDB(t)
	ctx := context.Background()

	const userID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	seedUploadTestDevice(t, userID, "upload-test-node-a", "tel-a", "device-token-a")

	device := &uploadDevice{NodeID: "upload-test-node-a", UserID: userID, TelescopeID: "tel-a"}
	sessionID, err := createUploadSession(ctx, device, "s3-upload-1", "1/2/frame.fits", "saucepan", uploadGradeMeta{
		CampaignID:  1,
		TaskID:      2,
		TelescopeID: "tel-a",
	})
	if err != nil {
		t.Fatalf("createUploadSession: %v", err)
	}

	loaded, err := getUploadSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("getUploadSession: %v", err)
	}
	if loaded.S3UploadID != "s3-upload-1" || loaded.NodeID != device.NodeID {
		t.Fatalf("unexpected session: %+v", loaded)
	}
}

func TestUploadPresignHTTPRejectsCrossDevice(t *testing.T) {
	setupUploadSessionTestDB(t)
	ctx := context.Background()

	const userA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	const userB = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	seedUploadTestDevice(t, userA, "upload-test-node-a", "tel-a", "device-token-a")
	seedUploadTestDevice(t, userB, "upload-test-node-b", "tel-b", "device-token-b")

	deviceA := &uploadDevice{NodeID: "upload-test-node-a", UserID: userA, TelescopeID: "tel-a"}
	sessionID, err := createUploadSession(ctx, deviceA, "s3-upload-cross", "1/2/frame.fits", "saucepan", uploadGradeMeta{
		TelescopeID: "tel-a",
	})
	if err != nil {
		t.Fatalf("createUploadSession: %v", err)
	}

	body, _ := json.Marshal(map[string]any{
		"upload_id":   sessionID,
		"part_number": 1,
	})
	req := httptest.NewRequest(http.MethodPost, "/upload/presign", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer device-token-b")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	requireUploadDevice(handleUploadPresign)(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestUploadPresignHTTPRejectsMissingAuth(t *testing.T) {
	body, _ := json.Marshal(map[string]any{"upload_id": uuid.New().String(), "part_number": 1})
	req := httptest.NewRequest(http.MethodPost, "/upload/presign", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	requireUploadDevice(handleUploadPresign)(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestListExpiredUploadSessions(t *testing.T) {
	setupUploadSessionTestDB(t)
	ctx := context.Background()

	const userID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	seedUploadTestDevice(t, userID, "upload-test-node-exp", "tel-exp", "device-token-exp")
	device := &uploadDevice{NodeID: "upload-test-node-exp", UserID: userID, TelescopeID: "tel-exp"}

	sessionID, err := createUploadSession(ctx, device, "s3-expired", "9/9/expired.fits", "saucepan", uploadGradeMeta{})
	if err != nil {
		t.Fatalf("createUploadSession: %v", err)
	}
	_, err = db.Exec(ctx, `UPDATE upload_sessions SET expires_at = NOW() - INTERVAL '1 minute' WHERE session_id = $1::uuid`, sessionID)
	if err != nil {
		t.Fatalf("backdate expiry: %v", err)
	}

	expired, err := listExpiredUploadSessions(ctx, 10)
	if err != nil {
		t.Fatalf("listExpiredUploadSessions: %v", err)
	}
	found := false
	for _, s := range expired {
		if s.SessionID == sessionID {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected expired session in sweep list")
	}
}

func TestResolveUploadTelescopeID(t *testing.T) {
	device := &uploadDevice{TelescopeID: "tel-bound"}
	got, err := resolveUploadTelescopeID(device, "")
	if err != nil || got != "tel-bound" {
		t.Fatalf("bound telescope: got=%q err=%v", got, err)
	}
	_, err = resolveUploadTelescopeID(device, "other-tel")
	if err != errUploadTelescopeMismatch {
		t.Fatalf("expected mismatch, got %v", err)
	}
}
