package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/saucepan/hotpath/shared/campaign"
)

func TestPauseResumeCampaign(t *testing.T) {
	setupCampaignTestDB(t)
	ctx := context.Background()
	userID := insertTestUser(t, ctx, "pause-resume@example.com", true)
	access, _, err := generateAccessToken(userID, "pause-resume@example.com")
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	var campaignID string
	err = db.QueryRow(ctx, `
		INSERT INTO campaigns (name, status, created_by, pack_json)
		VALUES ('pause-test', 'active', $1, '{}')
		RETURNING id::text
	`, userID).Scan(&campaignID)
	if err != nil {
		t.Fatalf("insert campaign: %v", err)
	}

	pause := httptest.NewRequest(http.MethodPost, "/api/v1/campaigns/"+campaignID+"/pause", nil)
	pause.SetPathValue("id", campaignID)
	pause.Header.Set("Authorization", "Bearer "+access)
	pauseRec := httptest.NewRecorder()
	requireResearcherJWT(handlePauseCampaign)(pauseRec, pause)
	if pauseRec.Code != http.StatusOK {
		t.Fatalf("pause status=%d body=%s", pauseRec.Code, pauseRec.Body.String())
	}

	var status string
	err = db.QueryRow(ctx, `SELECT status FROM campaigns WHERE id = $1::uuid`, campaignID).Scan(&status)
	if err != nil || status != campaign.StatusPaused {
		t.Fatalf("status after pause=%q", status)
	}

	resume := httptest.NewRequest(http.MethodPost, "/api/v1/campaigns/"+campaignID+"/resume", nil)
	resume.SetPathValue("id", campaignID)
	resume.Header.Set("Authorization", "Bearer "+access)
	resumeRec := httptest.NewRecorder()
	requireResearcherJWT(handleResumeCampaign)(resumeRec, resume)
	if resumeRec.Code != http.StatusOK {
		t.Fatalf("resume status=%d body=%s", resumeRec.Code, resumeRec.Body.String())
	}

	err = db.QueryRow(ctx, `SELECT status FROM campaigns WHERE id = $1::uuid`, campaignID).Scan(&status)
	if err != nil || status != campaign.StatusActive {
		t.Fatalf("status after resume=%q", status)
	}
}

func TestPauseCampaign_InvalidTransition(t *testing.T) {
	setupCampaignTestDB(t)
	ctx := context.Background()
	userID := insertTestUser(t, ctx, "pause-draft@example.com", true)
	access, _, err := generateAccessToken(userID, "pause-draft@example.com")
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	var campaignID string
	err = db.QueryRow(ctx, `
		INSERT INTO campaigns (name, status, created_by, pack_json)
		VALUES ('draft-test', 'draft', $1, '{}')
		RETURNING id::text
	`, userID).Scan(&campaignID)
	if err != nil {
		t.Fatalf("insert campaign: %v", err)
	}

	pause := httptest.NewRequest(http.MethodPost, "/api/v1/campaigns/"+campaignID+"/pause", nil)
	pause.SetPathValue("id", campaignID)
	pause.Header.Set("Authorization", "Bearer "+access)
	pauseRec := httptest.NewRecorder()
	requireResearcherJWT(handlePauseCampaign)(pauseRec, pause)
	if pauseRec.Code != http.StatusConflict {
		t.Fatalf("pause draft status=%d want 409", pauseRec.Code)
	}
}

func TestPauseCampaign_RejectsNonOwner(t *testing.T) {
	setupCampaignTestDB(t)
	ctx := context.Background()
	ownerID := insertTestUser(t, ctx, "campaign-owner@example.com", true)
	otherID := insertTestUser(t, ctx, "campaign-other@example.com", true)
	otherToken, _, err := generateAccessToken(otherID, "campaign-other@example.com")
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	var campaignID string
	err = db.QueryRow(ctx, `
		INSERT INTO campaigns (name, status, created_by, pack_json)
		VALUES ('owner-only', 'active', $1, '{}')
		RETURNING id::text
	`, ownerID).Scan(&campaignID)
	if err != nil {
		t.Fatalf("campaign: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/campaigns/"+campaignID+"/pause", nil)
	req.SetPathValue("id", campaignID)
	req.Header.Set("Authorization", "Bearer "+otherToken)
	rec := httptest.NewRecorder()
	requireResearcherJWT(handlePauseCampaign)(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403 body=%s", rec.Code, rec.Body.String())
	}
}

func TestMaybeCompleteCampaign(t *testing.T) {
	setupCampaignTestDB(t)
	ctx := context.Background()
	_, err := db.Exec(ctx, `
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
		)
	`)
	if err != nil {
		t.Fatalf("researcher_events: %v", err)
	}
	userID := insertTestUser(t, ctx, "complete@example.com", true)

	var campaignID string
	err = db.QueryRow(ctx, `
		INSERT INTO campaigns (name, status, created_by, pack_json)
		VALUES ('done-test', 'active', $1, '{}')
		RETURNING id::text
	`, userID).Scan(&campaignID)
	if err != nil {
		t.Fatalf("insert campaign: %v", err)
	}

	var taskID int
	err = db.QueryRow(ctx, `
		INSERT INTO tasks (name, status, campaign_id, normalized_integration_budget_s)
		VALUES ('only', 'completed', $1::uuid, 10)
		RETURNING id
	`, campaignID).Scan(&taskID)
	if err != nil {
		t.Fatalf("insert task: %v", err)
	}

	maybeCompleteCampaign(ctx, taskID)

	var status string
	err = db.QueryRow(ctx, `SELECT status FROM campaigns WHERE id = $1::uuid`, campaignID).Scan(&status)
	if err != nil || status != campaign.StatusCompleted {
		t.Fatalf("campaign status=%q want completed", status)
	}
}

func TestCampaignStackStatus(t *testing.T) {
	setupCampaignTestDB(t)
	ctx := context.Background()
	userID := insertTestUser(t, ctx, "stack-status@example.com", true)
	access, _, err := generateAccessToken(userID, "stack-status@example.com")
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	var campaignID string
	err = db.QueryRow(ctx, `
		INSERT INTO campaigns (name, status, created_by, pack_json)
		VALUES ('stack-test', 'active', $1, '{}')
		RETURNING id::text
	`, userID).Scan(&campaignID)
	if err != nil {
		t.Fatalf("insert campaign: %v", err)
	}

	var taskID int
	err = db.QueryRow(ctx, `
		INSERT INTO tasks (name, status, campaign_id) VALUES ('t1', 'pending', $1::uuid) RETURNING id
	`, campaignID).Scan(&taskID)
	if err != nil {
		t.Fatalf("task: %v", err)
	}
	_, err = db.Exec(ctx, `INSERT INTO telescopes (telescope_id) VALUES ('scope-1') ON CONFLICT DO NOTHING`)
	if err != nil {
		t.Fatalf("telescope: %v", err)
	}
	_, err = db.Exec(ctx, `
		INSERT INTO frame_grades (idempotency_key, task_id, telescope_id, dimensions, stack_eligible)
		VALUES
			($2, $1, 'scope-1', '{}'::jsonb, true),
			($3, $1, 'scope-1', '{}'::jsonb, false)
	`, taskID, "stack-k1-"+campaignID, "stack-k2-"+campaignID)
	if err != nil {
		t.Fatalf("grades: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/campaigns/"+campaignID+"/stack-status", nil)
	req.SetPathValue("id", campaignID)
	req.Header.Set("Authorization", "Bearer "+access)
	rec := httptest.NewRecorder()
	requireResearcherJWT(handleCampaignStackStatus)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("stack-status status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Tasks []struct {
			EligibleFrames int `json:"eligible_frames"`
			FrameCount     int `json:"frame_count"`
		} `json:"tasks"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Tasks) != 1 || body.Tasks[0].EligibleFrames != 1 || body.Tasks[0].FrameCount != 2 {
		t.Fatalf("unexpected stack status: %+v", body.Tasks)
	}
}
