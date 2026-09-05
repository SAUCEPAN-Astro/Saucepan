package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func setupObservationGroupTestDB(t *testing.T) {
	t.Helper()
	setupCampaignTestDB(t)
	ctx := context.Background()
	_, err := db.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS user_devices (
			user_id UUID NOT NULL, node_id TEXT NOT NULL,
			device_token_hash TEXT NOT NULL DEFAULT 'x', telescope_id TEXT,
			label TEXT NOT NULL DEFAULT '', created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (user_id, node_id)
		);
		CREATE TABLE IF NOT EXISTS observation_groups (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			campaign_id UUID REFERENCES campaigns(id) ON DELETE CASCADE,
			status TEXT NOT NULL DEFAULT 'locked', epoch_utc TIMESTAMPTZ NOT NULL,
			delta_t_max_s DOUBLE PRECISION NOT NULL DEFAULT 1,
			site_count INTEGER NOT NULL DEFAULT 2,
			min_projected_baseline_km DOUBLE PRECISION NOT NULL DEFAULT 0,
			projected_baseline_km DOUBLE PRECISION, target_ra DOUBLE PRECISION,
			target_dec DOUBLE PRECISION, pack_observation_mode JSONB NOT NULL DEFAULT '{}'::jsonb,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE TABLE IF NOT EXISTS observation_group_members (
			group_id UUID NOT NULL REFERENCES observation_groups(id) ON DELETE CASCADE,
			telescope_id TEXT NOT NULL, site_role TEXT NOT NULL DEFAULT 'member',
			status TEXT NOT NULL DEFAULT 'reserved', requested_mid_utc TIMESTAMPTZ,
			measured_mid_utc TIMESTAMPTZ, measured_timing_uncertainty_s DOUBLE PRECISION,
			site_latitude DOUBLE PRECISION, site_longitude DOUBLE PRECISION,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), PRIMARY KEY (group_id, telescope_id)
		);`)
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
}

func TestGetObservationGroup_RejectsNonOwner(t *testing.T) {
	setupObservationGroupTestDB(t)
	ctx := context.Background()
	ownerID := insertTestUser(t, ctx, "group-owner@example.com", true)
	otherID := insertTestUser(t, ctx, "group-other@example.com", true)
	otherToken, _, err := generateAccessToken(otherID, "group-other@example.com")
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	var campaignID, groupID string
	if err := db.QueryRow(ctx, `INSERT INTO campaigns (name, status, created_by, pack_json) VALUES ('parallax-secret', 'active', $1, '{}') RETURNING id::text`, ownerID).Scan(&campaignID); err != nil {
		t.Fatal(err)
	}
	epoch := time.Now().UTC().Add(2 * time.Hour)
	if err := db.QueryRow(ctx, `INSERT INTO observation_groups (campaign_id, status, epoch_utc, delta_t_max_s, site_count, min_projected_baseline_km, projected_baseline_km, target_ra, target_dec) VALUES ($1::uuid, 'locked', $2, 1.0, 2, 1000, 4500, 12.5, -3.25) RETURNING id::text`, campaignID, epoch).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO observation_group_members (group_id, telescope_id, site_role, site_latitude, site_longitude) VALUES ($1::uuid, 'west-pier', 'A', 40.0, -120.0), ($1::uuid, 'east-pier', 'B', 35.0, 10.0)`, groupID); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/observation-groups/"+groupID, nil)
	req.SetPathValue("id", groupID)
	req.Header.Set("Authorization", "Bearer "+otherToken)
	rec := httptest.NewRecorder()
	requireResearcherJWT(handleGetObservationGroup)(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404", rec.Code)
	}
}

func TestGetObservationGroup_AllowsOwner(t *testing.T) {
	setupObservationGroupTestDB(t)
	ctx := context.Background()
	ownerID := insertTestUser(t, ctx, "group-owner-ok@example.com", true)
	ownerToken, _, err := generateAccessToken(ownerID, "group-owner-ok@example.com")
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	var campaignID, groupID string
	if err := db.QueryRow(ctx, `INSERT INTO campaigns (name, status, created_by, pack_json) VALUES ('parallax-mine', 'active', $1, '{}') RETURNING id::text`, ownerID).Scan(&campaignID); err != nil {
		t.Fatal(err)
	}
	epoch := time.Now().UTC().Add(2 * time.Hour)
	if err := db.QueryRow(ctx, `INSERT INTO observation_groups (campaign_id, status, epoch_utc, delta_t_max_s, site_count, min_projected_baseline_km, projected_baseline_km, target_ra, target_dec) VALUES ($1::uuid, 'locked', $2, 1.0, 2, 1000, 4500, 12.5, -3.25) RETURNING id::text`, campaignID, epoch).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/observation-groups/"+groupID, nil)
	req.SetPathValue("id", groupID)
	req.Header.Set("Authorization", "Bearer "+ownerToken)
	rec := httptest.NewRecorder()
	requireResearcherJWT(handleGetObservationGroup)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rec.Code)
	}
}

func TestGetObservationGroup_AllowsMemberTelescopeOwner(t *testing.T) {
	setupObservationGroupTestDB(t)
	ctx := context.Background()
	ownerID := insertTestUser(t, ctx, "campaign-owner-2@example.com", true)
	memberID := insertTestUser(t, ctx, "pier-operator@example.com", true)
	memberToken, _, err := generateAccessToken(memberID, "pier-operator@example.com")
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	var campaignID, groupID string
	if err := db.QueryRow(ctx, `INSERT INTO campaigns (name, status, created_by, pack_json) VALUES ('parallax-member', 'active', $1, '{}') RETURNING id::text`, ownerID).Scan(&campaignID); err != nil {
		t.Fatal(err)
	}
	epoch := time.Now().UTC().Add(2 * time.Hour)
	if err := db.QueryRow(ctx, `INSERT INTO observation_groups (campaign_id, status, epoch_utc, delta_t_max_s, site_count, min_projected_baseline_km, projected_baseline_km, target_ra, target_dec) VALUES ($1::uuid, 'locked', $2, 1.0, 2, 1000, 4500, 12.5, -3.25) RETURNING id::text`, campaignID, epoch).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO observation_group_members (group_id, telescope_id, site_role, site_latitude, site_longitude) VALUES ($1::uuid, 'operator-pier', 'A', 40.0, -120.0)`, groupID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO user_devices (user_id, node_id, device_token_hash, telescope_id) VALUES ($1::uuid, 'node-1', 'test-hash', 'operator-pier')`, memberID); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/observation-groups/"+groupID, nil)
	req.SetPathValue("id", groupID)
	req.Header.Set("Authorization", "Bearer "+memberToken)
	rec := httptest.NewRecorder()
	requireResearcherJWT(handleGetObservationGroup)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rec.Code)
	}
}
