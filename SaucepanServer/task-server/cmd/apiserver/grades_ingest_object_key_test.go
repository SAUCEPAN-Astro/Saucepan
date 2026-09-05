package main

import (
	"context"
	"net/http"
	"strconv"
	"testing"
)

func TestCanonicalizeLandingObjectKey(t *testing.T) {
	got, err := canonicalizeLandingObjectKey(`camp\1\frame.fits`)
	if err != nil || got != "camp/1/frame.fits" {
		t.Fatalf("backslash normalize: got %q err=%v", got, err)
	}
	if _, err := canonicalizeLandingObjectKey("../evil/frame.fits"); err == nil {
		t.Fatal("expected reject for .. prefix")
	}
	if _, err := canonicalizeLandingObjectKey("a/../b/frame.fits"); err == nil {
		t.Fatal("expected reject for embedded ..")
	}
	if _, err := canonicalizeLandingObjectKey("/abs/frame.fits"); err == nil {
		t.Fatal("expected reject absolute key")
	}
	if _, err := canonicalizeLandingObjectKey("  "); err == nil {
		t.Fatal("expected reject empty")
	}
}

func TestValidateGradeIngestObjectKeysUnitNoDB(t *testing.T) {
	prev := db
	db = nil
	t.Cleanup(func() { db = prev })

	taskID := 42
	ctx := context.Background()

	err := validateGradeIngestObjectKeys(ctx, map[string]any{
		"object_key": "other_campaign/secret/stack.fits",
	}, &taskID, "node_001")
	if err == nil {
		t.Fatal("expected reject for foreign key")
	}

	err = validateGradeIngestObjectKeys(ctx, map[string]any{
		"object_key":        "1/42/frame.fits",
		"graded_object_key": "1/42/graded.fits",
		"quality_metrics":   map[string]any{"object_key": "1/42/frame.fits"},
	}, &taskID, "node_001")
	if err != nil {
		t.Fatalf("valid landing keys: %v", err)
	}

	err = validateGradeIngestObjectKeys(ctx, map[string]any{
		"object_key": "1/42/../../evil/x.fits",
	}, &taskID, "node_001")
	if err == nil {
		t.Fatal("expected reject for .. escape")
	}

	err = validateGradeIngestObjectKeys(ctx, map[string]any{
		"object_key": "1/42/frame.fits",
	}, nil, "node_001")
	if err == nil {
		t.Fatal("expected reject when task missing")
	}

	// Local staged paths are ignored (not object-store keys).
	err = validateGradeIngestObjectKeys(ctx, map[string]any{
		"staged_path": "/storage/staging/frame.fits",
	}, nil, "node_001")
	if err != nil {
		t.Fatalf("staged_path only should skip: %v", err)
	}
}

func TestIsObjectStoreKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want bool
	}{
		{"empty", "", false},
		{"whitespace only", "   ", false},
		{"relative key", "1/42/frame.fits", true},
		{"absolute fs path", "/storage/staging/frame.fits", false},
		{"url with scheme", "https://example.com/frame.fits", false},
		{"s3 style url", "s3://bucket/key", false},
		{"key containing :// mid-string", "weird://not/a/scheme/but/rejected", false},
		{"leading/trailing whitespace trimmed to relative", "  1/42/frame.fits  ", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isObjectStoreKey(tt.key); got != tt.want {
				t.Fatalf("isObjectStoreKey(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

func TestCollectGradeObjectKeys(t *testing.T) {
	got := collectGradeObjectKeys(map[string]any{
		"object_key":        "1/42/a.fits",
		"graded_object_key": "1/42/b.fits",
		"quality_metrics":   map[string]any{"object_key": "1/42/a.fits"}, // duplicate of object_key
	})
	want := []string{"1/42/a.fits", "1/42/b.fits"}
	if len(got) != len(want) {
		t.Fatalf("collectGradeObjectKeys = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("collectGradeObjectKeys[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

func TestCollectGradeObjectKeysEmpty(t *testing.T) {
	if got := collectGradeObjectKeys(map[string]any{}); len(got) != 0 {
		t.Fatalf("expected no keys from empty data, got %v", got)
	}
	if got := collectGradeObjectKeys(map[string]any{"staged_path": "/local/only.fits"}); len(got) != 0 {
		t.Fatalf("staged_path alone must not be collected as an object key, got %v", got)
	}
}

func TestCollectGradeObjectKeysIgnoresNonURLValues(t *testing.T) {
	got := collectGradeObjectKeys(map[string]any{
		"object_key":        "https://example.com/absolute-not-a-key.fits",
		"graded_object_key": "/absolute/fs/path.fits",
	})
	if len(got) != 0 {
		t.Fatalf("expected URL/absolute values to be excluded, got %v", got)
	}
}

func TestLandingPrefix(t *testing.T) {
	if got := landingPrefix("camp-1", 42); got != "camp-1/42/" {
		t.Fatalf("landingPrefix = %q, want %q", got, "camp-1/42/")
	}
	if got := landingPrefix("", 0); got != "/0/" {
		t.Fatalf("landingPrefix empty campaign = %q, want %q", got, "/0/")
	}
}

func TestObjectKeyUnderLandingPrefix(t *testing.T) {
	camp := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	if !objectKeyUnderLandingPrefix(camp+"/42/frame.fits", camp, 42) {
		t.Fatal("UUID campaign prefix should match")
	}
	if !objectKeyUnderLandingPrefix("9/42/frame.fits", camp, 42) {
		t.Fatal("numeric campaign + task segment should match")
	}
	if objectKeyUnderLandingPrefix("other/99/secret.fits", camp, 42) {
		t.Fatal("foreign task segment must not match")
	}
	if objectKeyUnderLandingPrefix("9/42", camp, 42) {
		t.Fatal("prefix alone without object name must not match")
	}
}

func TestValidateGradeIngestObjectKeysRejectsForeign(t *testing.T) {
	setupGradesTestDB(t)
	ctx := context.Background()

	var taskID int
	if err := db.QueryRow(ctx, `
		INSERT INTO tasks (accumulated_exposure_seconds, allow_emulator)
		VALUES (0, false) RETURNING id`).Scan(&taskID); err != nil {
		t.Fatalf("seed task: %v", err)
	}

	payload := sampleGradePayload("key-foreign-1")
	payload["task_id"] = taskID
	payload["object_key"] = "other_campaign/secret/stack.fits"
	payload["graded_object_key"] = "other_campaign/secret/stack.fits"

	body, status, err := ingestGradePayload(ctx, payload)
	if err != nil {
		t.Fatalf("ingestGradePayload: %v", err)
	}
	if status != http.StatusBadRequest {
		t.Fatalf("expected 400 for foreign key, got %d body=%v", status, body)
	}
}

func TestValidateGradeIngestObjectKeysRejectsDotDot(t *testing.T) {
	setupGradesTestDB(t)
	ctx := context.Background()

	var taskID int
	if err := db.QueryRow(ctx, `
		INSERT INTO tasks (accumulated_exposure_seconds, allow_emulator)
		VALUES (0, false) RETURNING id`).Scan(&taskID); err != nil {
		t.Fatalf("seed task: %v", err)
	}

	payload := sampleGradePayload("key-dotdot-1")
	payload["task_id"] = taskID
	payload["object_key"] = "camp/" + strconv.Itoa(taskID) + "/../../other/secret.fits"

	body, status, err := ingestGradePayload(ctx, payload)
	if err != nil {
		t.Fatalf("ingestGradePayload: %v", err)
	}
	if status != http.StatusBadRequest {
		t.Fatalf("expected 400 for .. key, got %d body=%v", status, body)
	}
}

func TestValidateGradeIngestObjectKeysAcceptsLandingPath(t *testing.T) {
	setupGradesTestDB(t)
	ctx := context.Background()

	var taskID int
	if err := db.QueryRow(ctx, `
		INSERT INTO tasks (accumulated_exposure_seconds, allow_emulator)
		VALUES (0, false) RETURNING id`).Scan(&taskID); err != nil {
		t.Fatalf("seed task: %v", err)
	}

	key := "1/" + strconv.Itoa(taskID) + "/frame.fits"
	payload := sampleGradePayload("key-ok-1")
	payload["task_id"] = taskID
	payload["object_key"] = key
	payload["graded_object_key"] = key

	body, status, err := ingestGradePayload(ctx, payload)
	if err != nil {
		t.Fatalf("ingestGradePayload: %v", err)
	}
	if status != http.StatusCreated {
		t.Fatalf("expected 201 for valid landing key, got %d body=%v", status, body)
	}
}

func TestValidateGradeIngestObjectKeysRequiresTaskWhenKeyPresent(t *testing.T) {
	setupGradesTestDB(t)
	ctx := context.Background()

	payload := sampleGradePayload("key-no-task-1")
	payload["object_key"] = "1/2/frame.fits"

	body, status, err := ingestGradePayload(ctx, payload)
	if err != nil {
		t.Fatalf("ingestGradePayload: %v", err)
	}
	if status != http.StatusBadRequest {
		t.Fatalf("expected 400 when key present without task, got %d body=%v", status, body)
	}
}

func TestValidateGradeIngestObjectKeysSkipsLocalStagedPath(t *testing.T) {
	setupGradesTestDB(t)
	ctx := context.Background()

	var taskID int
	if err := db.QueryRow(ctx, `
		INSERT INTO tasks (accumulated_exposure_seconds, allow_emulator)
		VALUES (0, false) RETURNING id`).Scan(&taskID); err != nil {
		t.Fatalf("seed task: %v", err)
	}

	payload := sampleGradePayload("key-staged-1")
	payload["task_id"] = taskID
	// staged_path alone is not validated as an object key; graded_object_key absent.
	payload["staged_path"] = "/storage/staging/camp/frame.fits"

	body, status, err := ingestGradePayload(ctx, payload)
	if err != nil {
		t.Fatalf("ingestGradePayload: %v", err)
	}
	if status != http.StatusCreated {
		t.Fatalf("expected 201 when only local staged_path, got %d body=%v", status, body)
	}
}
