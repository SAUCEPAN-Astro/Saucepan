package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func setupGradesTestDB(t *testing.T) {
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
		CREATE TABLE IF NOT EXISTS tasks (
			id SERIAL PRIMARY KEY,
			accumulated_exposure_seconds DOUBLE PRECISION NOT NULL DEFAULT 0,
			allow_emulator BOOLEAN NOT NULL DEFAULT false
		);
		CREATE TABLE IF NOT EXISTS telescopes (
			telescope_id TEXT PRIMARY KEY,
			reputation_stats JSONB DEFAULT '{}'
		);
		CREATE TABLE IF NOT EXISTS frame_grades (
			id SERIAL PRIMARY KEY,
			upload_id VARCHAR(64),
			idempotency_key VARCHAR(128) NOT NULL UNIQUE,
			task_id INTEGER REFERENCES tasks(id) ON DELETE SET NULL,
			telescope_id TEXT NOT NULL REFERENCES telescopes(telescope_id) ON DELETE CASCADE,
			telescope_external_id VARCHAR(100),
			headline_grade INTEGER NOT NULL DEFAULT 0,
			dimensions JSONB NOT NULL,
			points_earned DOUBLE PRECISION NOT NULL DEFAULT 0.0,
			points_breakdown JSONB,
			stack_eligible BOOLEAN NOT NULL DEFAULT true,
			sp_exptime DOUBLE PRECISION,
			grader_version VARCHAR(32),
			quality_metrics JSONB,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE TABLE IF NOT EXISTS frame_catalog (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			frame_id TEXT,
			upload_id TEXT UNIQUE,
			telescope_id TEXT NOT NULL,
			task_id TEXT,
			campaign_id TEXT,
			object_key TEXT NOT NULL,
			checksum_sha256 TEXT,
			date_obs TIMESTAMPTZ,
			mjd_obs DOUBLE PRECISION,
			ra_deg DOUBLE PRECISION,
			dec_deg DOUBLE PRECISION,
			filter TEXT,
			exptime_sec DOUBLE PRECISION,
			airmass DOUBLE PRECISION,
			fwhm_arcsec DOUBLE PRECISION,
			snr DOUBLE PRECISION,
			tier SMALLINT,
			calstat TEXT,
			phot_flag TEXT,
			headline_grade SMALLINT,
			stack_eligible BOOLEAN,
			grade_json JSONB,
			zp DOUBLE PRECISION,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
	`
	if _, err := pool.Exec(ctx, schema); err != nil {
		t.Fatalf("schema setup: %v", err)
	}
	_, _ = pool.Exec(ctx, `DELETE FROM frame_grades`)
	_, _ = pool.Exec(ctx, `DELETE FROM telescopes WHERE telescope_id = 'node_001'`)
	if _, err := pool.Exec(ctx, `
		INSERT INTO telescopes (telescope_id, reputation_stats)
		VALUES ('node_001', '{}') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatalf("seed telescope: %v", err)
	}
	db = pool
	t.Cleanup(func() {
		pool.Close()
		db = nil
	})
}

func sampleGradePayload(uploadID string) map[string]any {
	return map[string]any{
		"upload_id":       uploadID,
		"telescope_id":    "node_001",
		"idempotency_key": uploadID + ":grade",
		"headline":        75,
		"sp_exptime":      30.0,
		"grader_version":  "test",
		"dimensions": map[string]any{
			"image_quality": map[string]any{"score": 0.8},
			"task_fidelity": map[string]any{"score": 0.9},
			"timeliness":    map[string]any{"score": 0.7},
		},
	}
}

func TestGradesIngestTokenValid(t *testing.T) {
	t.Setenv("GRADES_INGEST_TOKEN", "test-secret")

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	if gradesIngestTokenValid(req) {
		t.Fatal("missing token should fail")
	}

	req.Header.Set("Authorization", "Bearer wrong")
	if gradesIngestTokenValid(req) {
		t.Fatal("wrong token should fail")
	}

	req.Header.Set("Authorization", "Bearer test-secret")
	if !gradesIngestTokenValid(req) {
		t.Fatal("valid Bearer token should pass")
	}

	req2 := httptest.NewRequest(http.MethodPost, "/", nil)
	req2.Header.Set("Grades-Ingest-Token", "test-secret")
	if !gradesIngestTokenValid(req2) {
		t.Fatal("Grades-Ingest-Token header should pass")
	}
}

func TestHandleGradesIngestUnauthorized(t *testing.T) {
	t.Setenv("GRADES_INGEST_TOKEN", "test-secret")

	body, _ := json.Marshal(sampleGradePayload("http-unauth"))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/grades/ingest", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handleGradesIngest(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestIngestGradePayloadPersistsAndUpdatesReputation(t *testing.T) {
	setupGradesTestDB(t)

	body, status, err := ingestGradePayload(context.Background(), sampleGradePayload("up-1"))
	if err != nil {
		t.Fatalf("ingestGradePayload: %v", err)
	}
	if status != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%v", status, body)
	}
	fg, _ := body["frame_grade"].(map[string]any)
	if fg["points_earned"].(float64) <= 0 {
		t.Fatalf("expected positive points, got %v", fg["points_earned"])
	}
	if fg["stack_eligible"] != true {
		t.Fatal("expected stack_eligible true")
	}
	rep, _ := body["reputation_stats"].(map[string]any)
	if intFromAny(rep["frame_count"]) != 1 {
		t.Fatalf("expected frame_count 1, got %v", rep["frame_count"])
	}
}

func TestIngestGradePayloadIdempotency(t *testing.T) {
	setupGradesTestDB(t)

	payload := sampleGradePayload("dup-1")
	_, status1, err := ingestGradePayload(context.Background(), payload)
	if err != nil || status1 != http.StatusCreated {
		t.Fatalf("first ingest: status=%d err=%v", status1, err)
	}
	_, status2, err := ingestGradePayload(context.Background(), payload)
	if err != nil {
		t.Fatalf("duplicate ingest: %v", err)
	}
	if status2 != http.StatusConflict {
		t.Fatalf("expected 409, got %d", status2)
	}
}

func TestHandleGradesIngestHTTP(t *testing.T) {
	setupGradesTestDB(t)
	t.Setenv("GRADES_INGEST_TOKEN", "test-secret")

	body, _ := json.Marshal(sampleGradePayload("http-2"))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/grades/ingest", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handleGradesIngest(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	fg, _ := resp["frame_grade"].(map[string]any)
	if fg["idempotency_key"] != "http-2:grade" {
		t.Fatalf("idempotency_key mismatch: %v", fg["idempotency_key"])
	}
}

func TestHandleTelescopePoints(t *testing.T) {
	setupGradesTestDB(t)

	_, _, _ = ingestGradePayload(context.Background(), sampleGradePayload("pts-1"))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/telescopes/node_001/points", nil)
	req.SetPathValue("id", "node_001")
	rec := httptest.NewRecorder()
	handleTelescopePoints(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if intFromAny(resp["frame_count"]) != 1 {
		t.Fatalf("expected frame_count 1, got %v", resp["frame_count"])
	}
	recent, _ := resp["recent_frames"].([]any)
	if len(recent) != 1 {
		t.Fatalf("expected 1 recent frame, got %d", len(recent))
	}
}

func TestResolveOAExptimeFromFidelity(t *testing.T) {
	dimensions := map[string]any{
		"task_fidelity": map[string]any{"exptime_ratio": 0.5},
	}
	data := map[string]any{"integration_time_requested": 60.0}
	got := resolveOAExptime(data, dimensions)
	if got != 30.0 {
		t.Fatalf("expected 30.0, got %v", got)
	}
}

func TestIngestGradePayloadPersistsEmulatorFlags(t *testing.T) {
	setupGradesTestDB(t)
	ctx := context.Background()

	var taskID int
	if err := db.QueryRow(ctx, `
		INSERT INTO tasks (accumulated_exposure_seconds, allow_emulator)
		VALUES (0, false) RETURNING id`).Scan(&taskID); err != nil {
		t.Fatalf("seed task: %v", err)
	}

	payload := sampleGradePayload("emu-flag-1")
	payload["sp_emulator"] = true
	payload["data_tier"] = "emulator"
	payload["science_eligible"] = false
	payload["task_id"] = taskID

	body, status, err := ingestGradePayload(ctx, payload)
	if err != nil {
		t.Fatalf("ingestGradePayload: %v", err)
	}
	if status != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%v", status, body)
	}
	fg, _ := body["frame_grade"].(map[string]any)
	if fg["sp_emulator"] != true {
		t.Fatalf("expected sp_emulator true, got %v", fg["sp_emulator"])
	}
	if fg["data_tier"] != "emulator" {
		t.Fatalf("expected data_tier emulator, got %v", fg["data_tier"])
	}
	if fg["science_eligible"] != false {
		t.Fatalf("expected science_eligible false, got %v", fg["science_eligible"])
	}
}

func TestIngestGradePayloadAllowsEmulatorOnSandboxTask(t *testing.T) {
	setupGradesTestDB(t)
	ctx := context.Background()

	var taskID int
	if err := db.QueryRow(ctx, `
		INSERT INTO tasks (accumulated_exposure_seconds, allow_emulator)
		VALUES (0, true) RETURNING id`).Scan(&taskID); err != nil {
		t.Fatalf("seed task: %v", err)
	}

	payload := sampleGradePayload("emu-sandbox-1")
	payload["sp_emulator"] = true
	payload["data_tier"] = "emulator"
	payload["science_eligible"] = false
	payload["task_id"] = taskID

	_, status, err := ingestGradePayload(ctx, payload)
	if err != nil {
		t.Fatalf("ingestGradePayload: %v", err)
	}
	if status != http.StatusCreated {
		t.Fatalf("expected 201, got %d", status)
	}
}
