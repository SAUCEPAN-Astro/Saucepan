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

	"github.com/jackc/pgx/v5/pgxpool"
)

func setupSessionTestDB(t *testing.T) {
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
		CREATE EXTENSION IF NOT EXISTS pgcrypto;
		CREATE TABLE IF NOT EXISTS users (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			username TEXT,
			email TEXT UNIQUE,
			password_hash TEXT NOT NULL DEFAULT 'x',
			email_verified BOOLEAN NOT NULL DEFAULT false,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		ALTER TABLE users ADD COLUMN IF NOT EXISTS username TEXT;
		ALTER TABLE users ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
		ALTER TABLE users ALTER COLUMN email DROP NOT NULL;
		CREATE UNIQUE INDEX IF NOT EXISTS idx_users_username_lower ON users (lower(username));

		CREATE TABLE IF NOT EXISTS user_sessions (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			token_hash TEXT NOT NULL,
			user_agent TEXT,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			expires_at TIMESTAMPTZ NOT NULL,
			revoked_at TIMESTAMPTZ,
			replaced_by UUID REFERENCES user_sessions(id) ON DELETE SET NULL
		);
		CREATE UNIQUE INDEX IF NOT EXISTS ux_user_sessions_token_hash ON user_sessions (token_hash);
		CREATE INDEX IF NOT EXISTS ix_user_sessions_user_active ON user_sessions (user_id) WHERE revoked_at IS NULL;
	`
	if _, err := pool.Exec(ctx, schema); err != nil {
		pool.Close()
		t.Fatalf("schema setup: %v", err)
	}

	db = pool
	t.Cleanup(func() {
		pool.Close()
		db = nil
	})
}

func TestHashRefreshTokenStable(t *testing.T) {
	a := hashRefreshToken("tok-a")
	b := hashRefreshToken("tok-a")
	c := hashRefreshToken("tok-b")
	if a != b {
		t.Fatalf("hash not stable")
	}
	if a == c {
		t.Fatalf("different tokens must not collide")
	}
	if len(a) != 64 {
		t.Fatalf("expected sha256 hex length 64, got %d", len(a))
	}
}

type tokenResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	User         struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	} `json:"user"`
}

func postJSON(t *testing.T, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "session-test")
	rec := httptest.NewRecorder()
	switch path {
	case "/auth/register":
		handleRegister(rec, req)
	case "/auth/login":
		handleLogin(rec, req)
	case "/auth/refresh":
		handleRefresh(rec, req)
	case "/auth/logout":
		handleLogout(rec, req)
	case "/auth/change-password":
		handleChangePassword(rec, req)
	default:
		t.Fatalf("unknown path %s", path)
	}
	return rec
}

func decodeTokenResp(t *testing.T, rec *httptest.ResponseRecorder) tokenResp {
	t.Helper()
	var out tokenResp
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	return out
}

func uniqueUser(t *testing.T) (username, password string) {
	t.Helper()
	username = "u" + time.Now().UTC().Format("150405.000000000")
	password = "password12"
	return username, password
}

// uniqueToken returns a refresh-token-shaped string that won't collide with
// token_hash rows left in the DB by earlier test runs (the schema's
// ux_user_sessions_token_hash index is unique and this DB is not reset
// between `go test` invocations against the same postgres instance).
func uniqueToken(t *testing.T, label string) string {
	t.Helper()
	return label + "-" + time.Now().UTC().Format("150405.000000000")
}

func TestRefreshRotatesAndInvalidatesOld(t *testing.T) {
	setupSessionTestDB(t)
	username, password := uniqueUser(t)

	rec := postJSON(t, "/auth/register", map[string]string{
		"username": username,
		"password": password,
	})
	if rec.Code != 201 {
		t.Fatalf("register: %d %s", rec.Code, rec.Body.String())
	}
	first := decodeTokenResp(t, rec)
	if first.RefreshToken == "" {
		t.Fatal("missing refresh token")
	}

	rec = postJSON(t, "/auth/refresh", map[string]string{
		"refresh_token": first.RefreshToken,
	})
	if rec.Code != 200 {
		t.Fatalf("refresh: %d %s", rec.Code, rec.Body.String())
	}
	second := decodeTokenResp(t, rec)
	if second.RefreshToken == "" || second.RefreshToken == first.RefreshToken {
		t.Fatalf("expected rotated refresh token")
	}

	// Rotated token must work once before any reuse probe.
	rec = postJSON(t, "/auth/refresh", map[string]string{
		"refresh_token": second.RefreshToken,
	})
	if rec.Code != 200 {
		t.Fatalf("rotated refresh should work: %d %s", rec.Code, rec.Body.String())
	}
	third := decodeTokenResp(t, rec)

	// Presenting a previous (revoked) refresh is reuse → revoke all.
	rec = postJSON(t, "/auth/refresh", map[string]string{
		"refresh_token": first.RefreshToken,
	})
	if rec.Code != 401 {
		t.Fatalf("old refresh should fail after rotation, got %d %s", rec.Code, rec.Body.String())
	}

	rec = postJSON(t, "/auth/refresh", map[string]string{
		"refresh_token": third.RefreshToken,
	})
	if rec.Code != 401 {
		t.Fatalf("reuse detection should revoke all; got %d %s", rec.Code, rec.Body.String())
	}
}

func TestChangePasswordRevokesSessions(t *testing.T) {
	setupSessionTestDB(t)
	username, password := uniqueUser(t)

	rec := postJSON(t, "/auth/register", map[string]string{
		"username": username,
		"password": password,
	})
	if rec.Code != 201 {
		t.Fatalf("register: %d %s", rec.Code, rec.Body.String())
	}
	tok := decodeTokenResp(t, rec)

	rec = postJSON(t, "/auth/change-password", map[string]string{
		"username":         username,
		"current_password": password,
		"new_password":     "newpass99",
	})
	if rec.Code != 200 {
		t.Fatalf("change-password: %d %s", rec.Code, rec.Body.String())
	}

	rec = postJSON(t, "/auth/refresh", map[string]string{
		"refresh_token": tok.RefreshToken,
	})
	if rec.Code != 401 {
		t.Fatalf("refresh after password change should fail, got %d %s", rec.Code, rec.Body.String())
	}

	rec = postJSON(t, "/auth/login", map[string]string{
		"username": username,
		"password": "newpass99",
	})
	if rec.Code != 200 {
		t.Fatalf("login with new password: %d %s", rec.Code, rec.Body.String())
	}
}

func TestLogoutRevokesSession(t *testing.T) {
	setupSessionTestDB(t)
	username, password := uniqueUser(t)

	rec := postJSON(t, "/auth/register", map[string]string{
		"username": username,
		"password": password,
	})
	if rec.Code != 201 {
		t.Fatalf("register: %d %s", rec.Code, rec.Body.String())
	}
	tok := decodeTokenResp(t, rec)

	rec = postJSON(t, "/auth/logout", map[string]string{
		"refresh_token": tok.RefreshToken,
	})
	if rec.Code != 200 {
		t.Fatalf("logout: %d %s", rec.Code, rec.Body.String())
	}

	rec = postJSON(t, "/auth/refresh", map[string]string{
		"refresh_token": tok.RefreshToken,
	})
	if rec.Code != 401 {
		t.Fatalf("refresh after logout should fail, got %d %s", rec.Code, rec.Body.String())
	}
}

// registerAndGetUserID is a small helper for the direct sessions.go unit
// tests below that need a real user row to satisfy the FK on user_sessions.
func registerAndGetUserID(t *testing.T) (userID, username, password string) {
	t.Helper()
	username, password = uniqueUser(t)
	rec := postJSON(t, "/auth/register", map[string]string{
		"username": username,
		"password": password,
	})
	if rec.Code != 201 {
		t.Fatalf("register: %d %s", rec.Code, rec.Body.String())
	}
	tok := decodeTokenResp(t, rec)
	return tok.User.ID, username, password
}

func TestCreateSession(t *testing.T) {
	setupSessionTestDB(t)
	userID, _, _ := registerAndGetUserID(t)
	ctx := context.Background()

	id, err := createSession(ctx, userID, uniqueToken(t, "some-refresh-token"), "ua-1", refreshExpiresAt())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty session id")
	}
}

func TestCreateSession_EmptyUserAgentStoredAsNull(t *testing.T) {
	setupSessionTestDB(t)
	userID, _, _ := registerAndGetUserID(t)
	ctx := context.Background()

	id, err := createSession(ctx, userID, uniqueToken(t, "another-refresh-token"), "", refreshExpiresAt())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var ua *string
	if err := db.QueryRow(ctx, `SELECT user_agent FROM user_sessions WHERE id = $1`, id).Scan(&ua); err != nil {
		t.Fatal(err)
	}
	if ua != nil {
		t.Fatalf("expected NULL user_agent for empty string, got %v", *ua)
	}
}

func TestCreateSession_DuplicateTokenHashFails(t *testing.T) {
	setupSessionTestDB(t)
	userID, _, _ := registerAndGetUserID(t)
	ctx := context.Background()

	dupToken := uniqueToken(t, "dup-token")
	if _, err := createSession(ctx, userID, dupToken, "ua", refreshExpiresAt()); err != nil {
		t.Fatalf("first insert should succeed: %v", err)
	}
	if _, err := createSession(ctx, userID, dupToken, "ua", refreshExpiresAt()); err == nil {
		t.Fatal("expected unique constraint violation on duplicate token hash")
	}
}

func TestCreateSession_UnknownUserFailsFK(t *testing.T) {
	setupSessionTestDB(t)
	ctx := context.Background()
	if _, err := createSession(ctx, "00000000-0000-0000-0000-000000000000", "orphan-token", "ua", refreshExpiresAt()); err == nil {
		t.Fatal("expected FK violation for unknown user id")
	}
}

func TestRotateSession_UnknownToken(t *testing.T) {
	setupSessionTestDB(t)
	userID, _, _ := registerAndGetUserID(t)
	ctx := context.Background()

	_, err := rotateSession(ctx, userID, "never-issued", "new-refresh", "ua", refreshExpiresAt())
	if !errors.Is(err, errSessionNotFound) {
		t.Fatalf("expected errSessionNotFound, got %v", err)
	}
}

func TestRotateSession_WrongOwner(t *testing.T) {
	setupSessionTestDB(t)
	userID, _, _ := registerAndGetUserID(t)
	otherUserID, _, _ := registerAndGetUserID(t)
	ctx := context.Background()

	refresh := uniqueToken(t, "owner-mismatch-token")
	if _, err := createSession(ctx, userID, refresh, "ua", refreshExpiresAt()); err != nil {
		t.Fatal(err)
	}

	_, err := rotateSession(ctx, otherUserID, refresh, "new-refresh", "ua", refreshExpiresAt())
	if !errors.Is(err, errSessionNotFound) {
		t.Fatalf("expected errSessionNotFound for owner mismatch, got %v", err)
	}
}

func TestRotateSession_Expired(t *testing.T) {
	setupSessionTestDB(t)
	userID, _, _ := registerAndGetUserID(t)
	ctx := context.Background()

	refresh := uniqueToken(t, "expired-token")
	hash := hashRefreshToken(refresh)
	_, err := db.Exec(ctx, `
		INSERT INTO user_sessions (user_id, token_hash, user_agent, expires_at)
		VALUES ($1, $2, 'ua', $3)
	`, userID, hash, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	_, err = rotateSession(ctx, userID, refresh, "new-refresh", "ua", refreshExpiresAt())
	if !errors.Is(err, errSessionExpired) {
		t.Fatalf("expected errSessionExpired, got %v", err)
	}
}

func TestRotateSession_PlainRevokedNotReuse(t *testing.T) {
	// A session revoked directly (no replaced_by, e.g. logout) must surface
	// errSessionRevoked, not errSessionReuse — reuse detection only applies
	// when the token was superseded by rotation.
	setupSessionTestDB(t)
	userID, _, _ := registerAndGetUserID(t)
	ctx := context.Background()

	refresh := uniqueToken(t, "logged-out-token")
	if err := revokeSessionByHashForTest(ctx, userID, refresh); err != nil {
		t.Fatal(err)
	}

	_, err := rotateSession(ctx, userID, refresh, "new-refresh", "ua", refreshExpiresAt())
	if !errors.Is(err, errSessionRevoked) {
		t.Fatalf("expected errSessionRevoked, got %v", err)
	}
}

// revokeSessionByHashForTest inserts then revokes a session in one step,
// mirroring logout's revoke-without-replacement path.
func revokeSessionByHashForTest(ctx context.Context, userID, refresh string) error {
	if _, err := createSession(ctx, userID, refresh, "ua", refreshExpiresAt()); err != nil {
		return err
	}
	return revokeSessionByHash(ctx, refresh)
}

func TestRevokeSessionByHash_UnknownTokenReturnsNotFound(t *testing.T) {
	setupSessionTestDB(t)
	ctx := context.Background()
	err := revokeSessionByHash(ctx, "token-that-was-never-issued")
	if !errors.Is(err, errSessionNotFound) {
		t.Fatalf("expected errSessionNotFound, got %v", err)
	}
}

func TestRevokeSessionByHash_AlreadyRevokedReturnsNotFound(t *testing.T) {
	setupSessionTestDB(t)
	userID, _, _ := registerAndGetUserID(t)
	ctx := context.Background()

	refresh := uniqueToken(t, "revoke-twice-token")
	if _, err := createSession(ctx, userID, refresh, "ua", refreshExpiresAt()); err != nil {
		t.Fatal(err)
	}
	if err := revokeSessionByHash(ctx, refresh); err != nil {
		t.Fatalf("first revoke should succeed: %v", err)
	}
	// Second revoke of the same (now-revoked) token hits the "revoked_at IS
	// NULL" filter and affects zero rows, same observable outcome as unknown.
	err := revokeSessionByHash(ctx, refresh)
	if !errors.Is(err, errSessionNotFound) {
		t.Fatalf("expected errSessionNotFound on second revoke, got %v", err)
	}
}

func TestRevokeAllUserSessions_NoActiveSessions(t *testing.T) {
	setupSessionTestDB(t)
	ctx := context.Background()
	username, _ := uniqueUser(t)

	// Insert the user row directly, bypassing handleRegister (which itself
	// issues and stores a session), so this user genuinely has zero sessions.
	var userID string
	err := db.QueryRow(ctx, `
		INSERT INTO users (username, email, password_hash, email_verified)
		VALUES ($1, NULL, 'x', false)
		RETURNING id
	`, username).Scan(&userID)
	if err != nil {
		t.Fatal(err)
	}

	n, err := revokeAllUserSessions(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("expected 0 revoked for user with no sessions yet, got %d", n)
	}
}

func TestRevokeAllUserSessions_UnknownUser(t *testing.T) {
	setupSessionTestDB(t)
	ctx := context.Background()
	n, err := revokeAllUserSessions(ctx, "00000000-0000-0000-0000-000000000000")
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("expected 0 revoked for unknown user, got %d", n)
	}
}

func TestRefreshExpiresAt(t *testing.T) {
	before := time.Now()
	got := refreshExpiresAt()
	diff := got.Sub(before)
	if diff < refreshTokenTTL-time.Second || diff > refreshTokenTTL+time.Second {
		t.Fatalf("expected ~%v from now, got diff %v", refreshTokenTTL, diff)
	}
}

func TestRevokeAllUserSessionsHelper(t *testing.T) {
	setupSessionTestDB(t)
	ctx := context.Background()
	username, password := uniqueUser(t)
	rec := postJSON(t, "/auth/register", map[string]string{
		"username": username,
		"password": password,
	})
	if rec.Code != 201 {
		t.Fatalf("register: %d %s", rec.Code, rec.Body.String())
	}
	tok := decodeTokenResp(t, rec)

	n, err := revokeAllUserSessions(ctx, tok.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Fatalf("expected >=1 revoked, got %d", n)
	}
	rec = postJSON(t, "/auth/refresh", map[string]string{
		"refresh_token": tok.RefreshToken,
	})
	if rec.Code != 401 {
		t.Fatalf("expected revoke-all to invalidate refresh, got %d", rec.Code)
	}
}
