package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func setupCampaignTestDB(t *testing.T) {
	t.Helper()
	setupResearcherTestDB(t)
	ctx := context.Background()
	schema := `
		CREATE TABLE IF NOT EXISTS campaigns (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'draft',
			created_by UUID,
			points_multiplier DOUBLE PRECISION NOT NULL DEFAULT 1.0,
			test_only BOOLEAN NOT NULL DEFAULT false,
			pack_json JSONB NOT NULL DEFAULT '{}',
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			expanded_at TIMESTAMPTZ,
			hook_approved BOOLEAN NOT NULL DEFAULT false,
			comp_stars JSONB NOT NULL DEFAULT '[]'::jsonb
		);
		CREATE TABLE IF NOT EXISTS tasks (
			id SERIAL PRIMARY KEY,
			name TEXT,
			priority INT NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'pending',
			integration_time DOUBLE PRECISION,
			normalized_integration_budget_s DOUBLE PRECISION,
			normalized_integration_earned_s DOUBLE PRECISION NOT NULL DEFAULT 0,
			required_filters TEXT[],
			target_ra DOUBLE PRECISION,
			target_dec DOUBLE PRECISION,
			allow_emulator BOOLEAN NOT NULL DEFAULT false,
			campaign_id UUID REFERENCES campaigns(id) ON DELETE SET NULL,
			public_id UUID NOT NULL DEFAULT gen_random_uuid()
		);
		ALTER TABLE tasks ADD COLUMN IF NOT EXISTS campaign_id UUID REFERENCES campaigns(id) ON DELETE SET NULL;
		ALTER TABLE tasks ADD COLUMN IF NOT EXISTS assigned_telescope_id TEXT;
		CREATE TABLE IF NOT EXISTS telescopes (
			telescope_id TEXT PRIMARY KEY
		);
		CREATE TABLE IF NOT EXISTS frame_grades (
			id SERIAL PRIMARY KEY,
			idempotency_key VARCHAR(128) NOT NULL UNIQUE,
			task_id INTEGER REFERENCES tasks(id) ON DELETE SET NULL,
			telescope_id TEXT NOT NULL REFERENCES telescopes(telescope_id) ON DELETE CASCADE,
			headline_grade INTEGER NOT NULL DEFAULT 0,
			dimensions JSONB NOT NULL DEFAULT '{}',
			points_earned DOUBLE PRECISION NOT NULL DEFAULT 0.0,
			stack_eligible BOOLEAN NOT NULL DEFAULT true,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		ALTER TABLE frame_grades ADD COLUMN IF NOT EXISTS stack_eligible BOOLEAN NOT NULL DEFAULT true;
		ALTER TABLE frame_grades ADD COLUMN IF NOT EXISTS sp_exptime DOUBLE PRECISION;
		ALTER TABLE frame_grades ADD COLUMN IF NOT EXISTS points_earned DOUBLE PRECISION NOT NULL DEFAULT 0.0;
	`
	if _, err := db.Exec(ctx, schema); err != nil {
		t.Fatalf("campaign schema: %v", err)
	}
}

func TestCreateCampaign_RejectsUnknownPackFields(t *testing.T) {
	setupCampaignTestDB(t)
	ctx := context.Background()
	userID := insertTestUser(t, ctx, "pack-extra@example.com", true)
	access, _, err := generateAccessToken(userID, "pack-extra@example.com")
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	pack := []byte(`{
		"name": "evil",
		"transform_table_ref": "file:///etc/passwd",
		"targets": [{"ra": 1, "dec": 2, "filters": ["R"], "exposure_sec": 10, "frame_count": 1}]
	}`)
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/campaigns", bytes.NewReader(pack))
	createReq.Header.Set("Authorization", "Bearer "+access)
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	requireResearcherJWT(handleCreateCampaign)(createRec, createReq)
	if createRec.Code != http.StatusBadRequest {
		t.Fatalf("create status=%d want 400 body=%s", createRec.Code, createRec.Body.String())
	}
	if !bytes.Contains(createRec.Body.Bytes(), []byte("transform_table_ref")) {
		t.Fatalf("body=%s want transform_table_ref", createRec.Body.String())
	}
}

func TestCreateCampaign_StoresCanonicalPackJSON(t *testing.T) {
	setupCampaignTestDB(t)
	ctx := context.Background()
	userID := insertTestUser(t, ctx, "pack-canon@example.com", true)
	access, _, err := generateAccessToken(userID, "pack-canon@example.com")
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	pack := []byte(`{
		"name": "canon",
		"test_only": true,
		"targets": [{"ra": 1, "dec": 2, "filters": ["R"], "exposure_sec": 10, "frame_count": 1}]
	}`)
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/campaigns", bytes.NewReader(pack))
	createReq.Header.Set("Authorization", "Bearer "+access)
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	requireResearcherJWT(handleCreateCampaign)(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", createRec.Code, createRec.Body.String())
	}
	var createResp struct {
		Campaign struct {
			ID string `json:"id"`
		} `json:"campaign"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	var packRaw []byte
	err = db.QueryRow(ctx, `SELECT pack_json FROM campaigns WHERE id = $1::uuid`, createResp.Campaign.ID).Scan(&packRaw)
	if err != nil {
		t.Fatalf("load pack_json: %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(packRaw, &obj); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := obj["transform_table_ref"]; ok {
		t.Fatalf("unexpected key in pack_json: %s", packRaw)
	}
	if obj["name"] != "canon" {
		t.Fatalf("pack_json=%s", packRaw)
	}
}

func TestPublishCampaign_RejectsStoredUnknownFields(t *testing.T) {
	setupCampaignTestDB(t)
	ctx := context.Background()
	userID := insertTestUser(t, ctx, "pack-pub-extra@example.com", true)
	access, _, err := generateAccessToken(userID, "pack-pub-extra@example.com")
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	var campaignID string
	err = db.QueryRow(ctx, `
		INSERT INTO campaigns (name, status, created_by, pack_json, test_only)
		VALUES ('polluted', 'draft', $1::uuid, $2::jsonb, true)
		RETURNING id::text
	`, userID, `{
		"name": "polluted",
		"transform_table_ref": "http://169.254.169.254/latest/meta-data/",
		"targets": [{"ra": 1, "dec": 2, "filters": ["R"], "exposure_sec": 10, "frame_count": 1}]
	}`).Scan(&campaignID)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	pubReq := httptest.NewRequest(http.MethodPost, "/api/v1/campaigns/"+campaignID+"/publish", nil)
	pubReq.SetPathValue("id", campaignID)
	pubReq.Header.Set("Authorization", "Bearer "+access)
	pubRec := httptest.NewRecorder()
	requireResearcherJWT(handlePublishCampaign)(pubRec, pubReq)
	if pubRec.Code != http.StatusBadRequest {
		t.Fatalf("publish status=%d want 400 body=%s", pubRec.Code, pubRec.Body.String())
	}
}

func TestPublishCampaign_GenericFixture(t *testing.T) {
	setupCampaignTestDB(t)
	ctx := context.Background()
	userID := insertTestUser(t, ctx, "campaign-pub@example.com", true)
	access, _, err := generateAccessToken(userID, "campaign-pub@example.com")
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	pack, err := os.ReadFile("../../shared/campaign/testdata/generic_campaign.json")
	if err != nil {
		t.Fatalf("read pack: %v", err)
	}

	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/campaigns", bytes.NewReader(pack))
	createReq.Header.Set("Authorization", "Bearer "+access)
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	requireResearcherJWT(handleCreateCampaign)(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", createRec.Code, createRec.Body.String())
	}

	var createResp struct {
		Campaign struct {
			ID string `json:"id"`
		} `json:"campaign"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	campaignID := createResp.Campaign.ID
	if campaignID == "" {
		t.Fatal("missing campaign id")
	}

	pubReq := httptest.NewRequest(http.MethodPost, "/api/v1/campaigns/"+campaignID+"/publish", nil)
	pubReq.SetPathValue("id", campaignID)
	pubReq.Header.Set("Authorization", "Bearer "+access)
	pubRec := httptest.NewRecorder()
	requireResearcherJWT(handlePublishCampaign)(pubRec, pubReq)
	if pubRec.Code != http.StatusOK {
		t.Fatalf("publish status=%d body=%s", pubRec.Code, pubRec.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/campaigns/"+campaignID+"/tasks", nil)
	listReq.SetPathValue("id", campaignID)
	listReq.Header.Set("Authorization", "Bearer "+access)
	listRec := httptest.NewRecorder()
	requireResearcherJWT(handleListCampaignTasks)(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list tasks status=%d body=%s", listRec.Code, listRec.Body.String())
	}

	var listResp struct {
		Tasks []Task `json:"tasks"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode tasks: %v", err)
	}
	if len(listResp.Tasks) != 1 {
		t.Fatalf("got %d tasks, want 1 from demo_m31", len(listResp.Tasks))
	}
	if listResp.Tasks[0].NormalizedIntegrationBudgetS != 60 {
		t.Fatalf("budget=%v want 60", listResp.Tasks[0].NormalizedIntegrationBudgetS)
	}
}

func TestPublishCampaign_ComputeHookRejected(t *testing.T) {
	setupCampaignTestDB(t)
	ctx := context.Background()
	userID := insertTestUser(t, ctx, "hook-deny@example.com", true)
	access, _, err := generateAccessToken(userID, "hook-deny@example.com")
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	pack := []byte(`{
		"name": "hooked",
		"hook_placement": "compute",
		"hook_image_ref": "ghcr.io/example/hook@sha256:abc",
		"targets": [{
			"ra": 1, "dec": 2, "filters": ["R"],
			"exposure_sec": 10, "frame_count": 1
		}]
	}`)

	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/campaigns", bytes.NewReader(pack))
	createReq.Header.Set("Authorization", "Bearer "+access)
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	requireResearcherJWT(handleCreateCampaign)(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", createRec.Code, createRec.Body.String())
	}

	var createResp struct {
		Campaign struct {
			ID string `json:"id"`
		} `json:"campaign"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("decode create: %v", err)
	}

	pubReq := httptest.NewRequest(http.MethodPost, "/api/v1/campaigns/"+createResp.Campaign.ID+"/publish", nil)
	pubReq.SetPathValue("id", createResp.Campaign.ID)
	pubReq.Header.Set("Authorization", "Bearer "+access)
	pubRec := httptest.NewRecorder()
	requireResearcherJWT(handlePublishCampaign)(pubRec, pubReq)
	if pubRec.Code != http.StatusForbidden {
		t.Fatalf("publish status=%d want 403 body=%s", pubRec.Code, pubRec.Body.String())
	}
}

func TestCreateCampaign_CompStarsPersisted(t *testing.T) {
	setupCampaignTestDB(t)
	ctx := context.Background()
	userID := insertTestUser(t, ctx, "comp-stars@example.com", true)
	access, _, err := generateAccessToken(userID, "comp-stars@example.com")
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	pack := []byte(`{
		"name": "vars",
		"comp_stars": [{"ra": 12.5, "dec": 45.0, "mag": 11.2, "band": "V"}],
		"targets": [{"ra": 1, "dec": 2, "filters": ["R"], "exposure_sec": 10, "frame_count": 1}]
	}`)

	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/campaigns", bytes.NewReader(pack))
	createReq.Header.Set("Authorization", "Bearer "+access)
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	requireResearcherJWT(handleCreateCampaign)(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", createRec.Code, createRec.Body.String())
	}

	var createResp struct {
		Campaign struct {
			ID string `json:"id"`
		} `json:"campaign"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("decode create: %v", err)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/campaigns/"+createResp.Campaign.ID, nil)
	getReq.SetPathValue("id", createResp.Campaign.ID)
	getReq.Header.Set("Authorization", "Bearer "+access)
	getRec := httptest.NewRecorder()
	requireResearcherJWT(handleGetCampaign)(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", getRec.Code, getRec.Body.String())
	}

	var getResp struct {
		Campaign struct {
			CompStars []map[string]interface{} `json:"comp_stars"`
		} `json:"campaign"`
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &getResp); err != nil {
		t.Fatalf("decode get: %v", err)
	}
	if len(getResp.Campaign.CompStars) != 1 {
		t.Fatalf("comp_stars=%v want 1 entry", getResp.Campaign.CompStars)
	}
}

func TestCampaignLeaderboard(t *testing.T) {
	setupCampaignTestDB(t)
	ctx := context.Background()
	userID := insertTestUser(t, ctx, "leaderboard@example.com", true)
	access, _, err := generateAccessToken(userID, "leaderboard@example.com")
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	pack := []byte(`{
		"name": "lb",
		"targets": [{"ra": 1, "dec": 2, "filters": ["R"], "exposure_sec": 10, "frame_count": 1}]
	}`)
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/campaigns", bytes.NewReader(pack))
	createReq.Header.Set("Authorization", "Bearer "+access)
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	requireResearcherJWT(handleCreateCampaign)(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", createRec.Code, createRec.Body.String())
	}
	var createResp struct {
		Campaign struct {
			ID string `json:"id"`
		} `json:"campaign"`
	}
	_ = json.Unmarshal(createRec.Body.Bytes(), &createResp)

	pubReq := httptest.NewRequest(http.MethodPost, "/api/v1/campaigns/"+createResp.Campaign.ID+"/publish", nil)
	pubReq.SetPathValue("id", createResp.Campaign.ID)
	pubReq.Header.Set("Authorization", "Bearer "+access)
	pubRec := httptest.NewRecorder()
	requireResearcherJWT(handlePublishCampaign)(pubRec, pubReq)
	if pubRec.Code != http.StatusOK {
		t.Fatalf("publish: %d %s", pubRec.Code, pubRec.Body.String())
	}

	var taskPK int
	err = db.QueryRow(ctx, `SELECT id FROM tasks WHERE campaign_id = $1::uuid LIMIT 1`, createResp.Campaign.ID).Scan(&taskPK)
	if err != nil {
		t.Fatalf("task pk: %v", err)
	}
	for _, scope := range []string{"scope_a", "scope_b"} {
		if _, err := db.Exec(ctx, `INSERT INTO telescopes (telescope_id) VALUES ($1) ON CONFLICT DO NOTHING`, scope); err != nil {
			t.Fatalf("telescope: %v", err)
		}
	}
	points := map[string]float64{"scope_a": 30, "scope_b": 50}
	for scope, pts := range points {
		_, err := db.Exec(ctx, `
			INSERT INTO frame_grades (idempotency_key, task_id, telescope_id, dimensions, points_earned)
			VALUES ($1, $2, $3, '{}', $4)
		`, scope+"-lb-"+createResp.Campaign.ID, taskPK, scope, pts)
		if err != nil {
			t.Fatalf("grade insert: %v", err)
		}
	}

	lbReq := httptest.NewRequest(http.MethodGet, "/api/v1/campaigns/"+createResp.Campaign.ID+"/leaderboard", nil)
	lbReq.SetPathValue("id", createResp.Campaign.ID)
	lbReq.Header.Set("Authorization", "Bearer "+access)
	lbRec := httptest.NewRecorder()
	requireResearcherJWT(handleCampaignLeaderboard)(lbRec, lbReq)
	if lbRec.Code != http.StatusOK {
		t.Fatalf("leaderboard: %d %s", lbRec.Code, lbRec.Body.String())
	}
	var lbResp struct {
		Entries []CampaignLeaderboardEntry `json:"entries"`
	}
	if err := json.Unmarshal(lbRec.Body.Bytes(), &lbResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(lbResp.Entries) != 2 {
		t.Fatalf("entries=%d want 2", len(lbResp.Entries))
	}
	if lbResp.Entries[0].TelescopeID != "scope_b" || lbResp.Entries[0].TotalPoints != 50 {
		t.Fatalf("first entry=%+v want scope_b/50", lbResp.Entries[0])
	}
}

func TestCampaignReadEndpoints_RequireAuth(t *testing.T) {
	setupCampaignTestDB(t)
	ctx := context.Background()
	ownerID := insertTestUser(t, ctx, "read-auth-owner@example.com", true)

	var campaignID string
	err := db.QueryRow(ctx, `
		INSERT INTO campaigns (name, status, created_by, pack_json)
		VALUES ('auth-read', 'draft', $1, '{"name":"auth-read"}')
		RETURNING id::text
	`, ownerID).Scan(&campaignID)
	if err != nil {
		t.Fatalf("insert campaign: %v", err)
	}

	paths := []struct {
		path    string
		handler http.HandlerFunc
	}{
		{"/api/v1/campaigns", handleListCampaigns},
		{"/api/v1/campaigns/" + campaignID, handleGetCampaign},
		{"/api/v1/campaigns/" + campaignID + "/tasks", handleListCampaignTasks},
		{"/api/v1/campaigns/" + campaignID + "/leaderboard", handleCampaignLeaderboard},
		{"/api/v1/campaigns/" + campaignID + "/stack-status", handleCampaignStackStatus},
	}
	for _, tc := range paths {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		if tc.path != "/api/v1/campaigns" {
			req.SetPathValue("id", campaignID)
		}
		rec := httptest.NewRecorder()
		requireResearcherJWT(tc.handler)(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status=%d want 401 body=%s", tc.path, rec.Code, rec.Body.String())
		}
	}
}

func TestCampaignReadEndpoints_RejectNonOwner(t *testing.T) {
	setupCampaignTestDB(t)
	ctx := context.Background()
	ownerID := insertTestUser(t, ctx, "read-owner@example.com", true)
	otherID := insertTestUser(t, ctx, "read-other@example.com", true)
	otherToken, _, err := generateAccessToken(otherID, "read-other@example.com")
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	var campaignID string
	err = db.QueryRow(ctx, `
		INSERT INTO campaigns (name, status, created_by, pack_json)
		VALUES ('owned', 'draft', $1, '{"secret":true}')
		RETURNING id::text
	`, ownerID).Scan(&campaignID)
	if err != nil {
		t.Fatalf("insert campaign: %v", err)
	}

	handlers := []struct {
		name    string
		path    string
		handler http.HandlerFunc
	}{
		{"get", "/api/v1/campaigns/" + campaignID, handleGetCampaign},
		{"tasks", "/api/v1/campaigns/" + campaignID + "/tasks", handleListCampaignTasks},
		{"leaderboard", "/api/v1/campaigns/" + campaignID + "/leaderboard", handleCampaignLeaderboard},
		{"stack-status", "/api/v1/campaigns/" + campaignID + "/stack-status", handleCampaignStackStatus},
	}
	for _, tc := range handlers {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		req.SetPathValue("id", campaignID)
		req.Header.Set("Authorization", "Bearer "+otherToken)
		rec := httptest.NewRecorder()
		requireResearcherJWT(tc.handler)(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: status=%d want 404 body=%s", tc.name, rec.Code, rec.Body.String())
		}
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/campaigns", nil)
	listReq.Header.Set("Authorization", "Bearer "+otherToken)
	listRec := httptest.NewRecorder()
	requireResearcherJWT(handleListCampaigns)(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listRec.Code, listRec.Body.String())
	}
	var listResp struct {
		Campaigns []Campaign `json:"campaigns"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	for _, c := range listResp.Campaigns {
		if c.ID == campaignID {
			t.Fatalf("list leaked owner campaign %s to non-owner", campaignID)
		}
	}
}

func TestCampaignReadEndpoints_OwnerOK(t *testing.T) {
	setupCampaignTestDB(t)
	ctx := context.Background()
	ownerID := insertTestUser(t, ctx, "read-ok-owner@example.com", true)
	access, _, err := generateAccessToken(ownerID, "read-ok-owner@example.com")
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	var campaignID string
	err = db.QueryRow(ctx, `
		INSERT INTO campaigns (name, status, created_by, pack_json)
		VALUES ('mine', 'draft', $1, '{"name":"mine"}')
		RETURNING id::text
	`, ownerID).Scan(&campaignID)
	if err != nil {
		t.Fatalf("insert campaign: %v", err)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/campaigns/"+campaignID, nil)
	getReq.SetPathValue("id", campaignID)
	getReq.Header.Set("Authorization", "Bearer "+access)
	getRec := httptest.NewRecorder()
	requireResearcherJWT(handleGetCampaign)(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", getRec.Code, getRec.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/campaigns", nil)
	listReq.Header.Set("Authorization", "Bearer "+access)
	listRec := httptest.NewRecorder()
	requireResearcherJWT(handleListCampaigns)(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listRec.Code, listRec.Body.String())
	}
	var listResp struct {
		Campaigns []Campaign `json:"campaigns"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	found := false
	for _, c := range listResp.Campaigns {
		if c.ID == campaignID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("owner list missing own campaign %s", campaignID)
	}
}
