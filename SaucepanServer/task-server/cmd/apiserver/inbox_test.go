package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func setupInboxTestDB(t *testing.T) {
	t.Helper()
	setupResearcherTestDB(t)
	ctx := context.Background()
	schema := `
		DROP TABLE IF EXISTS inbox_deliveries;
		CREATE TABLE IF NOT EXISTS campaigns (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			name TEXT NOT NULL,
			created_by UUID,
			status TEXT NOT NULL DEFAULT 'draft',
			description TEXT NOT NULL DEFAULT '',
			points_multiplier DOUBLE PRECISION NOT NULL DEFAULT 1.0,
			test_only BOOLEAN NOT NULL DEFAULT false,
			pack_json JSONB NOT NULL DEFAULT '{}',
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			expanded_at TIMESTAMPTZ
		);
		CREATE TABLE IF NOT EXISTS tasks (
			id SERIAL PRIMARY KEY,
			public_id UUID NOT NULL DEFAULT gen_random_uuid(),
			name TEXT NOT NULL DEFAULT '',
			priority INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'pending',
			campaign_id UUID REFERENCES campaigns(id)
		);
		CREATE TABLE IF NOT EXISTS inbox_deliveries (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			user_id UUID NOT NULL,
			campaign_id UUID NOT NULL,
			task_id INTEGER,
			task_public_id UUID,
			frame_grade_id INTEGER,
			upload_id VARCHAR(128),
			status TEXT NOT NULL DEFAULT 'completed',
			failure_reason TEXT,
			raw_object_key TEXT,
			graded_object_key TEXT,
			bucket TEXT NOT NULL DEFAULT 'saucepan',
			points_earned DOUBLE PRECISION,
			stack_eligible BOOLEAN,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			acked_at TIMESTAMPTZ
		);
		CREATE TABLE IF NOT EXISTS frame_grades (
			id SERIAL PRIMARY KEY,
			task_id INTEGER,
			telescope_id TEXT,
			dimensions JSONB NOT NULL DEFAULT '{}'::jsonb,
			stack_eligible BOOLEAN NOT NULL DEFAULT true
		);
	`
	if _, err := db.Exec(ctx, schema); err != nil {
		t.Fatalf("inbox schema: %v", err)
	}
}

func TestInboxPollAndAck(t *testing.T) {
	setupInboxTestDB(t)
	ctx := context.Background()
	userID := insertTestUser(t, ctx, "inbox@example.com", true)
	access, _, err := generateAccessToken(userID, "inbox@example.com")
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	var campaignID string
	err = db.QueryRow(ctx, `
		INSERT INTO campaigns (name, created_by, status)
		VALUES ('inbox-test', $1, 'active')
		RETURNING id::text
	`, userID).Scan(&campaignID)
	if err != nil {
		t.Fatalf("campaign: %v", err)
	}

	var taskID int
	err = db.QueryRow(ctx, `
		INSERT INTO tasks (name, campaign_id) VALUES ('t1', $1::uuid) RETURNING id
	`, campaignID).Scan(&taskID)
	if err != nil {
		t.Fatalf("task: %v", err)
	}

	var deliveryID string
	err = db.QueryRow(ctx, `
		INSERT INTO inbox_deliveries (
			user_id, campaign_id, task_id, status, raw_object_key, graded_object_key, points_earned
		) VALUES ($1::uuid, $2::uuid, $3, 'completed', '1/1/raw.fits', '1/1/graded.fits', 5.5)
		RETURNING id::text
	`, userID, campaignID, taskID).Scan(&deliveryID)
	if err != nil {
		t.Fatalf("delivery: %v", err)
	}

	pollReq := httptest.NewRequest(http.MethodGet, "/api/v1/inbox?campaign_id="+campaignID, nil)
	pollReq.Header.Set("Authorization", "Bearer "+access)
	pollRec := httptest.NewRecorder()
	handleInboxPoll(pollRec, pollReq)
	if pollRec.Code != http.StatusOK {
		t.Fatalf("poll status=%d body=%s", pollRec.Code, pollRec.Body.String())
	}

	ackReq := httptest.NewRequest(http.MethodPost, "/api/v1/inbox/"+deliveryID+"/ack", nil)
	ackReq.SetPathValue("id", deliveryID)
	ackReq.Header.Set("Authorization", "Bearer "+access)
	ackRec := httptest.NewRecorder()
	handleInboxAck(ackRec, ackReq)
	if ackRec.Code != http.StatusOK {
		t.Fatalf("ack status=%d body=%s", ackRec.Code, ackRec.Body.String())
	}
}
