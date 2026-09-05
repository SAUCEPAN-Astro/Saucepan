package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func setupOwnershipTestDB(t *testing.T) {
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
			aperture_mm DOUBLE PRECISION,
			qe DOUBLE PRECISION,
			focal_length_mm DOUBLE PRECISION,
			pixel_size_um DOUBLE PRECISION,
			site_latitude DOUBLE PRECISION,
			site_longitude DOUBLE PRECISION,
			median_seeing_arcsec DOUBLE PRECISION,
			fov_width_arcmin DOUBLE PRECISION,
			fov_height_arcmin DOUBLE PRECISION,
			mount_type INTEGER,
			max_stable_exposure_s DOUBLE PRECISION,
			obstruction_mask JSONB,
			mount_limits JSONB,
			horizon_profile JSONB,
			is_emulator BOOLEAN NOT NULL DEFAULT false,
			enabled_campaign_ids TEXT[] DEFAULT '{}',
			updated_at TIMESTAMPTZ DEFAULT NOW()
		);
		ALTER TABLE telescopes ADD COLUMN IF NOT EXISTS owner_user_id UUID REFERENCES users(id) ON DELETE SET NULL;
		ALTER TABLE telescopes ADD COLUMN IF NOT EXISTS aperture_mm DOUBLE PRECISION;
		ALTER TABLE telescopes ADD COLUMN IF NOT EXISTS qe DOUBLE PRECISION;
		ALTER TABLE telescopes ADD COLUMN IF NOT EXISTS focal_length_mm DOUBLE PRECISION;
		ALTER TABLE telescopes ADD COLUMN IF NOT EXISTS pixel_size_um DOUBLE PRECISION;
		ALTER TABLE telescopes ADD COLUMN IF NOT EXISTS site_latitude DOUBLE PRECISION;
		ALTER TABLE telescopes ADD COLUMN IF NOT EXISTS site_longitude DOUBLE PRECISION;
		ALTER TABLE telescopes ADD COLUMN IF NOT EXISTS median_seeing_arcsec DOUBLE PRECISION;
		ALTER TABLE telescopes ADD COLUMN IF NOT EXISTS fov_width_arcmin DOUBLE PRECISION;
		ALTER TABLE telescopes ADD COLUMN IF NOT EXISTS fov_height_arcmin DOUBLE PRECISION;
		ALTER TABLE telescopes ADD COLUMN IF NOT EXISTS mount_type INTEGER;
		ALTER TABLE telescopes ADD COLUMN IF NOT EXISTS max_stable_exposure_s DOUBLE PRECISION;
		ALTER TABLE telescopes ADD COLUMN IF NOT EXISTS obstruction_mask JSONB;
		ALTER TABLE telescopes ADD COLUMN IF NOT EXISTS mount_limits JSONB;
		ALTER TABLE telescopes ADD COLUMN IF NOT EXISTS horizon_profile JSONB;
		ALTER TABLE telescopes ADD COLUMN IF NOT EXISTS enabled_campaign_ids TEXT[] DEFAULT '{}';
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

	_, _ = pool.Exec(ctx, `DELETE FROM user_devices WHERE node_id LIKE 'own-%' OR telescope_id LIKE 'own-%'`)
	_, _ = pool.Exec(ctx, `DELETE FROM telescopes WHERE telescope_id LIKE 'own-%'`)
	_, _ = pool.Exec(ctx, `DELETE FROM users WHERE username LIKE 'own-%'`)

	db = pool
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM user_devices WHERE node_id LIKE 'own-%' OR telescope_id LIKE 'own-%'`)
		_, _ = pool.Exec(context.Background(), `DELETE FROM telescopes WHERE telescope_id LIKE 'own-%'`)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE username LIKE 'own-%'`)
		pool.Close()
		db = nil
	})
}

// Regression for #249: attacker POST /auth/devices with victim telescope_id
// must not become owner; legitimate JWT register claim still works.
func TestDeviceCreateDoesNotSquatTelescopeOwnership(t *testing.T) {
	setupOwnershipTestDB(t)
	ctx := context.Background()

	victimID := uuid.New().String()
	attackerID := uuid.New().String()
	_, err := db.Exec(ctx, `
		INSERT INTO users (id, username, password_hash, email_verified)
		VALUES ($1::uuid, 'own-victim', 'x', true), ($2::uuid, 'own-attacker', 'x', true)
	`, victimID, attackerID)
	if err != nil {
		t.Fatalf("seed users: %v", err)
	}

	const telescopeID = "own-scope-1"
	_, err = db.Exec(ctx, `
		INSERT INTO telescopes (telescope_id, name, power)
		VALUES ($1, 'Victim Pier', 0.5)
	`, telescopeID)
	if err != nil {
		t.Fatalf("seed unowned telescope: %v", err)
	}

	attackerToken, _, err := generateAccessToken(attackerID, "own-attacker")
	if err != nil {
		t.Fatalf("attacker token: %v", err)
	}

	body, _ := json.Marshal(map[string]string{"telescope_id": telescopeID, "label": "squat"})
	req := httptest.NewRequest(http.MethodPost, "/auth/devices", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+attackerToken)
	rec := httptest.NewRecorder()
	requireJWT(handleAuthDevicesCreate)(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 from /auth/devices, got %d body=%s", rec.Code, rec.Body.String())
	}

	var owner *string
	if err := db.QueryRow(ctx, `
		SELECT owner_user_id::text FROM telescopes WHERE telescope_id = $1
	`, telescopeID).Scan(&owner); err != nil {
		t.Fatalf("read owner: %v", err)
	}
	if owner != nil {
		t.Fatalf("POST /auth/devices must not stamp owner_user_id; got %s", *owner)
	}

	owns, err := userOwnsTelescope(ctx, attackerID, telescopeID)
	if err != nil {
		t.Fatalf("userOwnsTelescope attacker: %v", err)
	}
	if owns {
		t.Fatal("device link must not confer ownership on unowned telescope")
	}

	victimToken, _, err := generateAccessToken(victimID, "own-victim")
	if err != nil {
		t.Fatalf("victim token: %v", err)
	}
	regBody := `{"telescope_id":"own-scope-1","name":"Victim Pier","power":0.6}`
	regReq := httptest.NewRequest(http.MethodPost, "/quest/telescopes", bytes.NewBufferString(regBody))
	regReq.Header.Set("Content-Type", "application/json")
	regReq.Header.Set("Authorization", "Bearer "+victimToken)
	regRec := httptest.NewRecorder()
	requireDeviceOrJWT(handleRegisterTelescope)(regRec, regReq)
	if regRec.Code != http.StatusCreated {
		t.Fatalf("expected 201 from legitimate register claim, got %d body=%s", regRec.Code, regRec.Body.String())
	}

	if err := db.QueryRow(ctx, `
		SELECT owner_user_id::text FROM telescopes WHERE telescope_id = $1
	`, telescopeID).Scan(&owner); err != nil {
		t.Fatalf("read owner after claim: %v", err)
	}
	if owner == nil || *owner != victimID {
		t.Fatalf("legitimate claim should set owner to victim; got %v", owner)
	}

	ownsVictim, err := userOwnsTelescope(ctx, victimID, telescopeID)
	if err != nil {
		t.Fatalf("userOwnsTelescope victim: %v", err)
	}
	if !ownsVictim {
		t.Fatal("victim should own telescope after register claim")
	}
	ownsAttacker, err := userOwnsTelescope(ctx, attackerID, telescopeID)
	if err != nil {
		t.Fatalf("userOwnsTelescope attacker after claim: %v", err)
	}
	if ownsAttacker {
		t.Fatal("attacker must still not own after victim claim")
	}
}

func TestAssertCanClaimBlocksForeignOwner(t *testing.T) {
	setupOwnershipTestDB(t)
	ctx := context.Background()

	ownerID := uuid.New().String()
	otherID := uuid.New().String()
	_, err := db.Exec(ctx, `
		INSERT INTO users (id, username, password_hash, email_verified)
		VALUES ($1::uuid, 'own-owner', 'x', true), ($2::uuid, 'own-other', 'x', true)
	`, ownerID, otherID)
	if err != nil {
		t.Fatalf("seed users: %v", err)
	}
	_, err = db.Exec(ctx, `
		INSERT INTO telescopes (telescope_id, name, power, owner_user_id)
		VALUES ('own-scope-2', 'Owned', 0.5, $1::uuid)
	`, ownerID)
	if err != nil {
		t.Fatalf("seed telescope: %v", err)
	}

	if err := assertCanClaimTelescope(ctx, "own-scope-2", otherID); err == nil {
		t.Fatal("expected forbidden when another user owns telescope")
	} else if _, ok := err.(forbiddenError); !ok {
		t.Fatalf("expected forbiddenError, got %T: %v", err, err)
	}
	if err := assertCanClaimTelescope(ctx, "own-scope-2", ownerID); err != nil {
		t.Fatalf("owner should be allowed to claim: %v", err)
	}
}
