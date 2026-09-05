package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/saucepan/hotpath/shared/wire"
)

type recordingBoardPublisher struct {
	notes []wire.BoardNote
}

func (p *recordingBoardPublisher) Publish(note wire.BoardNote) error {
	p.notes = append(p.notes, note)
	return nil
}

func setupCampaignBoardTestDB(t *testing.T) {
	t.Helper()
	setupCampaignTestDB(t)
	if _, err := db.Exec(context.Background(), `
		DROP TABLE IF EXISTS campaign_board_notes;
		CREATE TABLE campaign_board_notes (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			campaign_id UUID NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
			author TEXT NOT NULL,
			event_type TEXT NOT NULL DEFAULT 'note',
			message TEXT NOT NULL DEFAULT '',
			payload JSONB NOT NULL DEFAULT '{}'::jsonb,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
	`); err != nil {
		t.Fatalf("board schema: %v", err)
	}
}

// seedCampaign inserts a campaign owned by owner and returns its id.
func seedCampaign(t *testing.T, owner string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(context.Background(),
		`INSERT INTO campaigns (name, status, created_by) VALUES ('board-camp', 'active', $1::uuid) RETURNING id::text`,
		owner,
	).Scan(&id); err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
	return id
}

func TestCampaignBoardPostAndPoll(t *testing.T) {
	setupCampaignBoardTestDB(t)
	previousPublisher := boardPublisher
	recorder := &recordingBoardPublisher{}
	boardPublisher = recorder
	t.Cleanup(func() { boardPublisher = previousPublisher })
	ctx := context.Background()
	owner := insertTestUser(t, ctx, "board-owner@example.com", true)
	access, _, err := generateAccessToken(owner, "board-owner@example.com")
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	campaignID := seedCampaign(t, owner)

	// empty board
	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/campaigns/"+campaignID+"/board", nil)
	getReq.SetPathValue("id", campaignID)
	getReq.Header.Set("Authorization", "Bearer "+access)
	getRec := httptest.NewRecorder()
	requireResearcherJWT(handleGetCampaignBoardNotes)(getRec, getReq)
	if getRec.Code != 200 {
		t.Fatalf("empty GET status=%d body=%s", getRec.Code, getRec.Body.String())
	}
	var empty struct {
		Notes []campaignBoardNote `json:"notes"`
	}
	_ = json.Unmarshal(getRec.Body.Bytes(), &empty)
	if len(empty.Notes) != 0 {
		t.Fatalf("want empty board, got %d", len(empty.Notes))
	}

	// post a note
	body := bytes.NewBufferString(`{"event_type":"note","message":"tile 3 looks clear","payload":{"snr":8.1}}`)
	postReq := httptest.NewRequest(http.MethodPost, "/api/v1/campaigns/"+campaignID+"/board", body)
	postReq.SetPathValue("id", campaignID)
	postReq.Header.Set("Authorization", "Bearer "+access)
	postReq.Header.Set("Content-Type", "application/json")
	postRec := httptest.NewRecorder()
	requireResearcherJWT(handlePostCampaignBoardNote)(postRec, postReq)
	if postRec.Code != 201 {
		t.Fatalf("POST status=%d body=%s", postRec.Code, postRec.Body.String())
	}
	var posted struct {
		Note      campaignBoardNote `json:"note"`
		Transport string            `json:"transport"`
	}
	if err := json.Unmarshal(postRec.Body.Bytes(), &posted); err != nil {
		t.Fatalf("decode posted: %v", err)
	}
	if posted.Note.Author != "researcher" || posted.Note.Message != "tile 3 looks clear" {
		t.Fatalf("posted note = %+v", posted.Note)
	}
	if posted.Transport != "mqtt_and_database" {
		t.Fatalf("transport = %q, want mqtt_and_database", posted.Transport)
	}
	if len(recorder.notes) != 1 {
		t.Fatalf("published notes = %d, want 1", len(recorder.notes))
	}
	published := recorder.notes[0]
	if published.CampaignID != campaignID || published.NodeID != "researcher" || published.EventType != "note" {
		t.Fatalf("published board note = %+v", published)
	}
	if published.Message != posted.Note.Message || published.SentAt.IsZero() {
		t.Fatalf("published board note timestamp/message = %+v", published)
	}
	if time.Since(published.SentAt) > time.Minute {
		t.Fatalf("published board note timestamp is too old: %s", published.SentAt)
	}

	// poll it back
	getRec2 := httptest.NewRecorder()
	getReq2 := httptest.NewRequest(http.MethodGet, "/api/v1/campaigns/"+campaignID+"/board", nil)
	getReq2.SetPathValue("id", campaignID)
	getReq2.Header.Set("Authorization", "Bearer "+access)
	requireResearcherJWT(handleGetCampaignBoardNotes)(getRec2, getReq2)
	var got struct {
		Notes []campaignBoardNote `json:"notes"`
	}
	if err := json.Unmarshal(getRec2.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode poll: %v", err)
	}
	if len(got.Notes) != 1 || got.Notes[0].ID != posted.Note.ID {
		t.Fatalf("poll = %+v, want the one posted note", got.Notes)
	}
	if string(got.Notes[0].Payload) != `{"snr": 8.1}` && string(got.Notes[0].Payload) != `{"snr":8.1}` {
		t.Fatalf("payload round-trip = %s", got.Notes[0].Payload)
	}
}

func TestCampaignBoardRejectsNonOwner(t *testing.T) {
	setupCampaignBoardTestDB(t)
	ctx := context.Background()
	owner := insertTestUser(t, ctx, "board-real-owner@example.com", true)
	campaignID := seedCampaign(t, owner)

	intruder := insertTestUser(t, ctx, "board-intruder@example.com", true)
	access, _, _ := generateAccessToken(intruder, "board-intruder@example.com")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/campaigns/"+campaignID+"/board",
		bytes.NewBufferString(`{"message":"let me in"}`))
	req.SetPathValue("id", campaignID)
	req.Header.Set("Authorization", "Bearer "+access)
	rec := httptest.NewRecorder()
	requireResearcherJWT(handlePostCampaignBoardNote)(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("intruder POST status=%d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

func TestCampaignBoardAcceptsEmptyOpaqueMessage(t *testing.T) {
	setupCampaignBoardTestDB(t)
	ctx := context.Background()
	owner := insertTestUser(t, ctx, "board-empty@example.com", true)
	access, _, _ := generateAccessToken(owner, "board-empty@example.com")
	campaignID := seedCampaign(t, owner)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/campaigns/"+campaignID+"/board",
		bytes.NewBufferString(`{"event_type":"note","message":"  "}`))
	req.SetPathValue("id", campaignID)
	req.Header.Set("Authorization", "Bearer "+access)
	rec := httptest.NewRecorder()
	requireResearcherJWT(handlePostCampaignBoardNote)(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("empty message status=%d, want 201; body=%s", rec.Code, rec.Body.String())
	}
}
