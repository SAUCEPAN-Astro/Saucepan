package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTelescopePatchRejectsUnauthenticated(t *testing.T) {
	body := `{"telescope_id":"auth-tel-unauth","power":0.5}`
	req := httptest.NewRequest(http.MethodPatch, "/quest/telescopes/auth-tel-unauth", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "auth-tel-unauth")
	rec := httptest.NewRecorder()

	requireDeviceOrJWT(handleRegisterTelescope)(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthenticated PATCH, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestTelescopePostRejectsUnauthenticated(t *testing.T) {
	body := `{"telescope_id":"auth-tel-unauth-post","power":0.5}`
	req := httptest.NewRequest(http.MethodPost, "/quest/telescopes", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	requireDeviceOrJWT(handleRegisterTelescope)(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthenticated POST, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func setupTelescopeAuthTestDB(t *testing.T) {
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
		CREATE TABLE IF NOT EXISTS telescopes (
			telescope_id TEXT PRIMARY KEY,
			name TEXT DEFAULT '',
			power DOUBLE PRECISION DEFAULT 0.5,
			available_filters TEXT[] DEFAULT '{}',
			is_emulator BOOLEAN NOT NULL DEFAULT false,
			enabled_campaign_ids TEXT[] DEFAULT '{}',
			updated_at TIMESTAMPTZ DEFAULT NOW()
		);
		ALTER TABLE telescopes ADD COLUMN IF NOT EXISTS owner_user_id UUID REFERENCES users(id) ON DELETE SET NULL;
		ALTER TABLE telescopes ADD COLUMN IF NOT EXISTS qe DOUBLE PRECISION;
		ALTER TABLE telescopes ADD COLUMN IF NOT EXISTS obstruction_mask JSONB;
		ALTER TABLE telescopes ADD COLUMN IF NOT EXISTS mount_limits JSONB;
		ALTER TABLE telescopes ADD COLUMN IF NOT EXISTS horizon_profile JSONB;
		ALTER TABLE telescopes ADD COLUMN IF NOT EXISTS site_latitude DOUBLE PRECISION;
		ALTER TABLE telescopes ADD COLUMN IF NOT EXISTS site_longitude DOUBLE PRECISION;
		ALTER TABLE telescopes ADD COLUMN IF NOT EXISTS aperture_mm DOUBLE PRECISION;
		ALTER TABLE telescopes ADD COLUMN IF NOT EXISTS focal_length_mm DOUBLE PRECISION;
		ALTER TABLE telescopes ADD COLUMN IF NOT EXISTS pixel_size_um DOUBLE PRECISION;
		ALTER TABLE telescopes ADD COLUMN IF NOT EXISTS median_seeing_arcsec DOUBLE PRECISION;
		ALTER TABLE telescopes ADD COLUMN IF NOT EXISTS fov_width_arcmin DOUBLE PRECISION;
		ALTER TABLE telescopes ADD COLUMN IF NOT EXISTS fov_height_arcmin DOUBLE PRECISION;
		ALTER TABLE telescopes ADD COLUMN IF NOT EXISTS mount_type INTEGER;
		ALTER TABLE telescopes ADD COLUMN IF NOT EXISTS max_stable_exposure_s DOUBLE PRECISION;
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
	`
	if _, err := pool.Exec(ctx, schema); err != nil {
		t.Fatalf("schema setup: %v", err)
	}

	_, _ = pool.Exec(ctx, `DELETE FROM telescopes WHERE telescope_id LIKE 'auth-tel-%'`)
	_, _ = pool.Exec(ctx, `DELETE FROM user_devices WHERE node_id LIKE 'auth-tel-%'`)
	_, _ = pool.Exec(ctx, `DELETE FROM users WHERE username LIKE 'auth-tel-%'`)

	db = pool
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM telescopes WHERE telescope_id LIKE 'auth-tel-%'`)
		_, _ = pool.Exec(context.Background(), `DELETE FROM user_devices WHERE node_id LIKE 'auth-tel-%'`)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE username LIKE 'auth-tel-%'`)
		pool.Close()
		db = nil
	})
}

func TestTelescopePatchRequiresOwnership(t *testing.T) {
	setupTelescopeAuthTestDB(t)
	ctx := context.Background()

	ownerID := uuid.New().String()
	otherID := uuid.New().String()
	_, err := db.Exec(ctx, `
		INSERT INTO users (id, username, password_hash, email_verified)
		VALUES ($1::uuid, 'auth-tel-owner', 'x', true), ($2::uuid, 'auth-tel-other', 'x', true)
	`, ownerID, otherID)
	if err != nil {
		t.Fatalf("seed users: %v", err)
	}
	_, err = db.Exec(ctx, `
		INSERT INTO telescopes (telescope_id, name, power, owner_user_id)
		VALUES ('auth-tel-owned', 'Owned', 0.5, $1::uuid)
	`, ownerID)
	if err != nil {
		t.Fatalf("seed telescope: %v", err)
	}

	otherToken, _, err := generateAccessToken(otherID, "auth-tel-other")
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	body := `{"telescope_id":"auth-tel-owned","power":0.9,"name":"hijack"}`
	req := httptest.NewRequest(http.MethodPatch, "/quest/telescopes/auth-tel-owned", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+otherToken)
	req.SetPathValue("id", "auth-tel-owned")
	rec := httptest.NewRecorder()
	requireDeviceOrJWT(handleRegisterTelescope)(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-owner PATCH, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestTelescopeReregisterWithoutCoordsPreservesSiteCoords guards #454: a
// telescope that previously registered with site coordinates must keep them
// when it re-registers (e.g. on client restart) without resending
// site_latitude/site_longitude. Before the *float64 fix, an omitted field
// unmarshaled to 0.0 (not NULL), so the upsert's COALESCE always picked the
// zero and silently wiped the stored location.
func TestTelescopeReregisterWithoutCoordsPreservesSiteCoords(t *testing.T) {
	setupTelescopeAuthTestDB(t)
	ctx := context.Background()

	ownerID := uuid.New().String()
	_, err := db.Exec(ctx, `
		INSERT INTO users (id, username, password_hash, email_verified)
		VALUES ($1::uuid, 'auth-tel-owner3', 'x', true)
	`, ownerID)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	token, _, err := generateAccessToken(ownerID, "auth-tel-owner3")
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	// First registration: send real site coordinates.
	body1 := `{"telescope_id":"auth-tel-coords","power":0.5,"site_latitude":51.5,"site_longitude":-0.1}`
	req1 := httptest.NewRequest(http.MethodPost, "/quest/telescopes", bytes.NewBufferString(body1))
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("Authorization", "Bearer "+token)
	rec1 := httptest.NewRecorder()
	requireDeviceOrJWT(handleRegisterTelescope)(rec1, req1)
	if rec1.Code != http.StatusCreated {
		t.Fatalf("expected 201 for first registration, got %d body=%s", rec1.Code, rec1.Body.String())
	}

	// Second registration: coords omitted entirely (client resent other fields only).
	body2 := `{"telescope_id":"auth-tel-coords","power":0.6}`
	req2 := httptest.NewRequest(http.MethodPost, "/quest/telescopes", bytes.NewBufferString(body2))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Authorization", "Bearer "+token)
	rec2 := httptest.NewRecorder()
	requireDeviceOrJWT(handleRegisterTelescope)(rec2, req2)
	if rec2.Code != http.StatusCreated {
		t.Fatalf("expected 201 for second registration, got %d body=%s", rec2.Code, rec2.Body.String())
	}

	var lat, lon *float64
	if err := db.QueryRow(ctx, `SELECT site_latitude, site_longitude FROM telescopes WHERE telescope_id = 'auth-tel-coords'`).Scan(&lat, &lon); err != nil {
		t.Fatalf("query stored coords: %v", err)
	}
	if lat == nil || lon == nil {
		t.Fatal("site coordinates were wiped to NULL by re-registration without coords")
	}
	if *lat != 51.5 || *lon != -0.1 {
		t.Fatalf("expected preserved coords (51.5, -0.1), got (%v, %v)", *lat, *lon)
	}
}

func TestTelescopePatchAllowsOwnerJWT(t *testing.T) {
	setupTelescopeAuthTestDB(t)
	ctx := context.Background()

	ownerID := uuid.New().String()
	_, err := db.Exec(ctx, `
		INSERT INTO users (id, username, password_hash, email_verified)
		VALUES ($1::uuid, 'auth-tel-owner2', 'x', true)
	`, ownerID)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	_, err = db.Exec(ctx, `
		INSERT INTO telescopes (telescope_id, name, power, owner_user_id)
		VALUES ('auth-tel-owned2', 'Owned', 0.5, $1::uuid)
	`, ownerID)
	if err != nil {
		t.Fatalf("seed telescope: %v", err)
	}

	token, _, err := generateAccessToken(ownerID, "auth-tel-owner2")
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	body := `{"telescope_id":"auth-tel-owned2","power":0.8,"name":"Updated"}`
	req := httptest.NewRequest(http.MethodPatch, "/quest/telescopes/auth-tel-owned2", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.SetPathValue("id", "auth-tel-owned2")
	rec := httptest.NewRecorder()
	requireDeviceOrJWT(handleRegisterTelescope)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for owner PATCH, got %d body=%s", rec.Code, rec.Body.String())
	}
}
