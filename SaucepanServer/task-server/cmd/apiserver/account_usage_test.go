package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAccountUsage_FromGrades(t *testing.T) {
	setupCampaignTestDB(t)
	ctx := context.Background()
	userID := insertTestUser(t, ctx, "usage-owner@example.com", true)
	otherID := insertTestUser(t, ctx, "usage-other@example.com", true)
	access, _, err := generateAccessToken(userID, "usage-owner@example.com")
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	var myCampaign, otherCampaign string
	err = db.QueryRow(ctx, `
		INSERT INTO campaigns (name, status, created_by, pack_json)
		VALUES ('my-usage', 'active', $1, '{}') RETURNING id::text
	`, userID).Scan(&myCampaign)
	if err != nil {
		t.Fatalf("campaign: %v", err)
	}
	err = db.QueryRow(ctx, `
		INSERT INTO campaigns (name, status, created_by, pack_json)
		VALUES ('other-usage', 'active', $1, '{}') RETURNING id::text
	`, otherID).Scan(&otherCampaign)
	if err != nil {
		t.Fatalf("other campaign: %v", err)
	}

	var myTask, otherTask int
	err = db.QueryRow(ctx, `
		INSERT INTO tasks (name, status, campaign_id, normalized_integration_budget_s, normalized_integration_earned_s)
		VALUES ('t-mine', 'pending', $1::uuid, 100, 25) RETURNING id
	`, myCampaign).Scan(&myTask)
	if err != nil {
		t.Fatalf("my task: %v", err)
	}
	err = db.QueryRow(ctx, `
		INSERT INTO tasks (name, status, campaign_id, normalized_integration_budget_s, normalized_integration_earned_s)
		VALUES ('t-other', 'pending', $1::uuid, 999, 999) RETURNING id
	`, otherCampaign).Scan(&otherTask)
	if err != nil {
		t.Fatalf("other task: %v", err)
	}

	_, _ = db.Exec(ctx, `INSERT INTO telescopes (telescope_id) VALUES ('usage-scope') ON CONFLICT DO NOTHING`)
	_, err = db.Exec(ctx, `
		INSERT INTO frame_grades (idempotency_key, task_id, telescope_id, dimensions, points_earned, stack_eligible, sp_exptime)
		VALUES
			($1, $2, 'usage-scope', '{}'::jsonb, 10.5, true, 30),
			($3, $2, 'usage-scope', '{}'::jsonb, 2.0, false, 5),
			($4, $5, 'usage-scope', '{}'::jsonb, 100, true, 500)
	`, "usage-k1-"+myCampaign, myTask, "usage-k2-"+myCampaign, "usage-k3-"+otherCampaign, otherTask)
	if err != nil {
		t.Fatalf("grades: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/account/usage", nil)
	req.Header.Set("Authorization", "Bearer "+access)
	rec := httptest.NewRecorder()
	requireResearcherJWT(handleAccountUsage)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	var body struct {
		Totals struct {
			FramesGraded        int64   `json:"frames_graded"`
			FramesStackEligible int64   `json:"frames_stack_eligible"`
			PointsEarned        float64 `json:"points_earned"`
			OAExptimeS          float64 `json:"sp_exptime_s"`
			NormalizedBudgetS   float64 `json:"normalized_integration_budget_s"`
			NormalizedEarnedS   float64 `json:"normalized_integration_earned_s"`
			Campaigns           int     `json:"campaigns"`
		} `json:"totals"`
		Campaigns []struct {
			CampaignID   string  `json:"campaign_id"`
			FramesGraded int64   `json:"frames_graded"`
			PointsEarned float64 `json:"points_earned"`
		} `json:"campaigns"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Totals.FramesGraded != 2 || body.Totals.FramesStackEligible != 1 {
		t.Fatalf("frames totals=%+v want graded=2 eligible=1", body.Totals)
	}
	if body.Totals.PointsEarned != 12.5 {
		t.Fatalf("points=%v want 12.5", body.Totals.PointsEarned)
	}
	if body.Totals.OAExptimeS != 35 {
		t.Fatalf("exptime=%v want 35", body.Totals.OAExptimeS)
	}
	if body.Totals.NormalizedBudgetS != 100 || body.Totals.NormalizedEarnedS != 25 {
		t.Fatalf("budget totals=%+v", body.Totals)
	}
	if body.Totals.Campaigns != 1 {
		t.Fatalf("campaigns=%d want 1 (only owned)", body.Totals.Campaigns)
	}
	found := false
	for _, c := range body.Campaigns {
		if c.CampaignID == myCampaign {
			found = true
			if c.FramesGraded != 2 || c.PointsEarned != 12.5 {
				t.Fatalf("my campaign breakdown=%+v", c)
			}
		}
		if c.CampaignID == otherCampaign {
			t.Fatal("must not include other user's campaign")
		}
	}
	if !found {
		t.Fatal("missing my campaign in breakdown")
	}
}

func TestAccountUsage_SinceFilter(t *testing.T) {
	setupCampaignTestDB(t)
	ctx := context.Background()
	userID := insertTestUser(t, ctx, "usage-since@example.com", true)
	access, _, err := generateAccessToken(userID, "usage-since@example.com")
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	var campaignID string
	_ = db.QueryRow(ctx, `
		INSERT INTO campaigns (name, status, created_by, pack_json)
		VALUES ('since-camp', 'active', $1, '{}') RETURNING id::text
	`, userID).Scan(&campaignID)
	var taskID int
	_ = db.QueryRow(ctx, `
		INSERT INTO tasks (name, status, campaign_id) VALUES ('t', 'pending', $1::uuid) RETURNING id
	`, campaignID).Scan(&taskID)
	_, _ = db.Exec(ctx, `INSERT INTO telescopes (telescope_id) VALUES ('usage-scope-2') ON CONFLICT DO NOTHING`)
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err = db.Exec(ctx, `
		INSERT INTO frame_grades (idempotency_key, task_id, telescope_id, dimensions, points_earned, stack_eligible, sp_exptime, created_at)
		VALUES ($1, $2, 'usage-scope-2', '{}'::jsonb, 1, true, 1, $3)
	`, "old-"+campaignID, taskID, old)
	if err != nil {
		t.Fatalf("old grade: %v", err)
	}
	_, err = db.Exec(ctx, `
		INSERT INTO frame_grades (idempotency_key, task_id, telescope_id, dimensions, points_earned, stack_eligible, sp_exptime)
		VALUES ($1, $2, 'usage-scope-2', '{}'::jsonb, 7, true, 10)
	`, "new-"+campaignID, taskID)
	if err != nil {
		t.Fatalf("new grade: %v", err)
	}

	since := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/account/usage?since="+since, nil)
	req.Header.Set("Authorization", "Bearer "+access)
	rec := httptest.NewRecorder()
	requireResearcherJWT(handleAccountUsage)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Totals struct {
			FramesGraded int64   `json:"frames_graded"`
			PointsEarned float64 `json:"points_earned"`
		} `json:"totals"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if body.Totals.FramesGraded != 1 || body.Totals.PointsEarned != 7 {
		t.Fatalf("since filter totals=%+v want 1 frame / 7 points", body.Totals)
	}
}
