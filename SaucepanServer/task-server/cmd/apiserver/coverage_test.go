package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func setupCoverageTestDB(t *testing.T) {
	t.Helper()
	setupCampaignTestDB(t)
	ctx := context.Background()
	schema := `
		ALTER TABLE tasks ADD COLUMN IF NOT EXISTS scheduled_end_at TIMESTAMPTZ;
		ALTER TABLE tasks ADD COLUMN IF NOT EXISTS handoff_lead_seconds INT;
		ALTER TABLE tasks ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ DEFAULT NOW();
		ALTER TABLE telescopes ADD COLUMN IF NOT EXISTS is_active BOOLEAN NOT NULL DEFAULT true;
		ALTER TABLE telescopes ADD COLUMN IF NOT EXISTS site_latitude DOUBLE PRECISION;
		ALTER TABLE telescopes ADD COLUMN IF NOT EXISTS site_longitude DOUBLE PRECISION;
		ALTER TABLE telescopes ADD COLUMN IF NOT EXISTS power DOUBLE PRECISION DEFAULT 0.5;
		ALTER TABLE telescopes ADD COLUMN IF NOT EXISTS is_emulator BOOLEAN NOT NULL DEFAULT false;
		CREATE TABLE IF NOT EXISTS researcher_events (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			user_id UUID NOT NULL,
			kind TEXT NOT NULL,
			event_type TEXT NOT NULL DEFAULT '',
			message TEXT NOT NULL,
			campaign_id UUID,
			task_id INTEGER,
			payload JSONB NOT NULL DEFAULT '{}',
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			acked_at TIMESTAMPTZ
		);
	`
	if _, err := db.Exec(ctx, schema); err != nil {
		t.Fatalf("coverage schema: %v", err)
	}
}

func TestCoverageSetPreviewApply(t *testing.T) {
	setupCoverageTestDB(t)
	ctx := context.Background()
	userID := insertTestUser(t, ctx, "coverage@example.com", true)
	access, _, err := generateAccessToken(userID, "coverage@example.com")
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	pack := map[string]any{
		"name": "coverage-test",
		"targets": []map[string]any{
			{"ra": 10.68, "dec": 41.27, "filters": []string{"L"}, "exposure_sec": 60, "frame_count": 10},
		},
	}
	packJSON, _ := json.Marshal(pack)

	var campaignID string
	err = db.QueryRow(ctx, `
		INSERT INTO campaigns (name, status, created_by, pack_json, test_only)
		VALUES ('coverage-test', 'active', $1, $2::jsonb, true)
		RETURNING id::text
	`, userID, string(packJSON)).Scan(&campaignID)
	if err != nil {
		t.Fatalf("insert campaign: %v", err)
	}

	_, err = db.Exec(ctx, `
		INSERT INTO tasks (name, status, campaign_id, normalized_integration_budget_s)
		VALUES ('t1', 'pending', $1::uuid, 3600)
	`, campaignID)
	if err != nil {
		t.Fatalf("insert task: %v", err)
	}

	_, _ = db.Exec(ctx, `DELETE FROM telescopes WHERE telescope_id LIKE 'cov-%'`)
	_, err = db.Exec(ctx, `
		INSERT INTO telescopes (telescope_id, is_active, site_latitude, site_longitude, power, is_emulator)
		VALUES
			('cov-a', true, 40, -120, 0.8, false),
			('cov-b', true, 35, 10, 0.7, false),
			('cov-c', true, 45, 140, 0.6, false)
	`)
	if err != nil {
		t.Fatalf("insert telescopes: %v", err)
	}

	intentBody := []byte(`{"enabled":true,"n_main":2,"redundancy":true}`)
	setReq := httptest.NewRequest(http.MethodPost, "/api/v1/campaigns/"+campaignID+"/coverage", bytes.NewReader(intentBody))
	setReq.SetPathValue("id", campaignID)
	setReq.Header.Set("Authorization", "Bearer "+access)
	setRec := httptest.NewRecorder()
	requireResearcherJWT(handleSetCampaignCoverage)(setRec, setReq)
	if setRec.Code != http.StatusOK {
		t.Fatalf("set coverage status=%d body=%s", setRec.Code, setRec.Body.String())
	}

	var packRaw []byte
	err = db.QueryRow(ctx, `SELECT pack_json FROM campaigns WHERE id = $1::uuid`, campaignID).Scan(&packRaw)
	if err != nil {
		t.Fatalf("read pack: %v", err)
	}
	var stored map[string]any
	_ = json.Unmarshal(packRaw, &stored)
	cov, _ := stored["coverage"].(map[string]any)
	if cov == nil || cov["enabled"] != true {
		t.Fatalf("pack coverage not persisted: %v", stored["coverage"])
	}

	prevReq := httptest.NewRequest(http.MethodPost, "/api/v1/campaigns/"+campaignID+"/coverage/preview", nil)
	prevReq.SetPathValue("id", campaignID)
	prevReq.Header.Set("Authorization", "Bearer "+access)
	prevRec := httptest.NewRecorder()
	requireResearcherJWT(handlePreviewCampaignCoverage)(prevRec, prevReq)
	if prevRec.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", prevRec.Code, prevRec.Body.String())
	}
	var prev map[string]any
	_ = json.Unmarshal(prevRec.Body.Bytes(), &prev)
	plan, _ := prev["plan"].(map[string]any)
	primary, _ := plan["primary"].([]any)
	if len(primary) < 1 {
		t.Fatalf("preview expected primary sites, got %v", prevRec.Body.String())
	}

	applyReq := httptest.NewRequest(http.MethodPost, "/api/v1/campaigns/"+campaignID+"/coverage/apply", bytes.NewReader(intentBody))
	applyReq.SetPathValue("id", campaignID)
	applyReq.Header.Set("Authorization", "Bearer "+access)
	applyRec := httptest.NewRecorder()
	requireResearcherJWT(handleApplyCampaignCoverage)(applyRec, applyReq)
	if applyRec.Code != http.StatusOK {
		t.Fatalf("apply status=%d body=%s", applyRec.Code, applyRec.Body.String())
	}

	err = db.QueryRow(ctx, `SELECT pack_json FROM campaigns WHERE id = $1::uuid`, campaignID).Scan(&packRaw)
	if err != nil {
		t.Fatalf("read pack after apply: %v", err)
	}
	_ = json.Unmarshal(packRaw, &stored)
	planStored, _ := stored["coverage_plan"].(map[string]any)
	if planStored == nil {
		t.Fatalf("coverage_plan not persisted: %v", stored)
	}
	planPrimary, _ := planStored["primary"].([]any)
	if len(planPrimary) < 1 {
		t.Fatalf("coverage_plan.primary empty: %v", planStored)
	}

	var endAt *string
	err = db.QueryRow(ctx, `
		SELECT scheduled_end_at::text FROM tasks WHERE campaign_id = $1::uuid LIMIT 1
	`, campaignID).Scan(&endAt)
	if err != nil || endAt == nil || *endAt == "" {
		t.Fatalf("expected scheduled_end_at after apply, err=%v end=%v", err, endAt)
	}
}

func TestCoverageRequiresResearcher(t *testing.T) {
	setupCoverageTestDB(t)
	ctx := context.Background()
	userID := insertTestUser(t, ctx, "cov-unapproved@example.com", false)
	access, _, err := generateAccessToken(userID, "cov-unapproved@example.com")
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	var campaignID string
	err = db.QueryRow(ctx, `
		INSERT INTO campaigns (name, status, created_by, pack_json)
		VALUES ('cov-forbid', 'active', $1, '{}')
		RETURNING id::text
	`, userID).Scan(&campaignID)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/campaigns/"+campaignID+"/coverage",
		bytes.NewReader([]byte(`{"enabled":true,"n_main":1}`)))
	req.SetPathValue("id", campaignID)
	req.Header.Set("Authorization", "Bearer "+access)
	rec := httptest.NewRecorder()
	requireResearcherJWT(handleSetCampaignCoverage)(rec, req)
	if rec.Code != http.StatusForbidden && rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401/403 for unapproved researcher, got %d", rec.Code)
	}
}
