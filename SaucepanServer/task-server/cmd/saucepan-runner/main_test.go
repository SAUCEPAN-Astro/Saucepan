package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saucepan/hotpath/shared/consent"
)

// withConsent points the runner at a temp consent file that approves
// campaignID for actions, so the #517 gate passes.
func withConsent(t *testing.T, campaignID string, actions ...string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "consent.json")
	s := &consent.Store{Campaigns: map[string]consent.Record{}}
	s.Approve(campaignID, actions)
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	t.Setenv(consent.EnvOverride, path)
}

// decodeRecords splits the runner's stdout into RunnerRecords.
func decodeRecords(t *testing.T, out string) []RunnerRecord {
	t.Helper()
	var recs []RunnerRecord
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		var rec RunnerRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("bad record line %q: %v", line, err)
		}
		recs = append(recs, rec)
	}
	return recs
}

func artifactSHA256(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestRunWellFormedJobRunsArtifactToDone(t *testing.T) {
	withConsent(t, "camp-1", "read_frame", "board_post")
	// A real (no-op, no-import) wasm module: passes compile + import allow-list
	// + run-export checks and returns cleanly.
	artifact := filepath.Join(t.TempDir(), "artifact.wasm")
	if err := os.WriteFile(artifact, buildWasm(nil, nil, noopBody(), true), 0o644); err != nil {
		t.Fatal(err)
	}
	job := RunnerJob{
		CampaignID:     "camp-1",
		TaskID:         "task-1",
		ArtifactPath:   artifact,
		ArtifactSHA256: artifactSHA256(t, artifact),
		Grants:         map[string]bool{"read_frame": true, "board_post": true},
	}
	in, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if code := run(t.Context(), bytes.NewReader(in), &out); code != 0 {
		t.Fatalf("exit code = %d, want 0 (out: %s)", code, out.String())
	}
	recs := decodeRecords(t, out.String())
	if len(recs) != 1 || recs[0].Action != ActionDone || !recs[0].OK {
		t.Fatalf("records = %+v, want one done/ok", recs)
	}
}

func TestRunWithoutConsentEmitsError(t *testing.T) {
	// Point at an empty consent file so the campaign is not approved.
	empty := filepath.Join(t.TempDir(), "empty-consent.json")
	if err := (&consent.Store{Campaigns: map[string]consent.Record{}}).Save(empty); err != nil {
		t.Fatal(err)
	}
	t.Setenv(consent.EnvOverride, empty)

	artifact := filepath.Join(t.TempDir(), "a.wasm")
	_ = os.WriteFile(artifact, []byte("\x00asm"), 0o644)
	job := RunnerJob{CampaignID: "camp-unapproved", ArtifactPath: artifact}
	job.ArtifactSHA256 = artifactSHA256(t, artifact)
	in, _ := json.Marshal(job)

	var out bytes.Buffer
	if code := run(t.Context(), bytes.NewReader(in), &out); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	recs := decodeRecords(t, out.String())
	if len(recs) != 1 || recs[0].Action != ActionError || !strings.Contains(recs[0].Msg, "consent") {
		t.Fatalf("records = %+v, want one consent error", recs)
	}
}

func TestRunConsentTooNarrowEmitsError(t *testing.T) {
	withConsent(t, "camp-2", "read_frame") // operator approved read only
	artifact := filepath.Join(t.TempDir(), "a.wasm")
	_ = os.WriteFile(artifact, []byte("\x00asm"), 0o644)
	job := RunnerJob{
		CampaignID:     "camp-2",
		ArtifactPath:   artifact,
		ArtifactSHA256: artifactSHA256(t, artifact),
		Grants:         map[string]bool{"read_frame": true, "next_capture": true}, // campaign wants more
	}
	in, _ := json.Marshal(job)

	var out bytes.Buffer
	if code := run(t.Context(), bytes.NewReader(in), &out); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	recs := decodeRecords(t, out.String())
	if len(recs) != 1 || recs[0].Action != ActionError {
		t.Fatalf("records = %+v, want one error", recs)
	}
}

func TestRunKillSwitchSkipsExecution(t *testing.T) {
	// No consent record, no artifact on disk — the kill switch short-circuits
	// before either is checked.
	job := RunnerJob{
		CampaignID:       "camp-killed",
		ArtifactPath:     "/no/such/artifact.wasm",
		PierCodeDisabled: true,
		Grants:           map[string]bool{"read_frame": true},
	}
	in, _ := json.Marshal(job)

	var out bytes.Buffer
	if code := run(t.Context(), bytes.NewReader(in), &out); code != 0 {
		t.Fatalf("exit code = %d, want 0 (disabled campaign is not an error); out: %s", code, out.String())
	}
	recs := decodeRecords(t, out.String())
	if len(recs) != 1 || recs[0].Action != ActionDone || !recs[0].OK {
		t.Fatalf("records = %+v, want one done/ok skip", recs)
	}
	if !strings.Contains(recs[0].Msg, "disabled") || !strings.Contains(recs[0].Msg, "camp-killed") {
		t.Fatalf("skip record should name the disabled campaign, got %q", recs[0].Msg)
	}
}

func TestRunMalformedJSONEmitsError(t *testing.T) {
	var out bytes.Buffer
	if code := run(t.Context(), strings.NewReader("{not json"), &out); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	recs := decodeRecords(t, out.String())
	if len(recs) != 1 || recs[0].Action != ActionError || recs[0].Msg == "" {
		t.Fatalf("records = %+v, want one error with a message", recs)
	}
}

func TestRunMissingArtifactEmitsError(t *testing.T) {
	job := RunnerJob{CampaignID: "camp-1", ArtifactPath: "/no/such/artifact.wasm"}
	in, _ := json.Marshal(job)

	var out bytes.Buffer
	if code := run(t.Context(), bytes.NewReader(in), &out); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	recs := decodeRecords(t, out.String())
	if len(recs) != 1 || recs[0].Action != ActionError {
		t.Fatalf("records = %+v, want one error", recs)
	}
	if !strings.Contains(recs[0].Msg, "artifact_path") {
		t.Fatalf("error msg %q should name artifact_path", recs[0].Msg)
	}
}

func TestRunRejectsMissingArtifactSHA256(t *testing.T) {
	withConsent(t, "camp-missing-hash", "read_frame")
	artifact := filepath.Join(t.TempDir(), "a.wasm")
	if err := os.WriteFile(artifact, []byte("actual bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	job := RunnerJob{CampaignID: "camp-missing-hash", ArtifactPath: artifact}
	in, _ := json.Marshal(job)

	var out bytes.Buffer
	if code := run(t.Context(), bytes.NewReader(in), &out); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	recs := decodeRecords(t, out.String())
	if len(recs) != 1 || recs[0].Action != ActionError || !strings.Contains(recs[0].Msg, "artifact_sha256") {
		t.Fatalf("records = %+v, want missing hash error", recs)
	}
}

func TestRunVerifiesArtifactSHA256WhenSet(t *testing.T) {
	withConsent(t, "camp-hash", "read_frame")
	body := buildWasm(nil, nil, noopBody(), true)
	sum := sha256.Sum256(body)
	artifact := filepath.Join(t.TempDir(), "a.wasm")
	if err := os.WriteFile(artifact, body, 0o644); err != nil {
		t.Fatal(err)
	}
	job := RunnerJob{
		CampaignID:     "camp-hash",
		ArtifactPath:   artifact,
		ArtifactSHA256: hex.EncodeToString(sum[:]),
		Grants:         map[string]bool{"read_frame": true},
	}
	in, _ := json.Marshal(job)

	var out bytes.Buffer
	if code := run(t.Context(), bytes.NewReader(in), &out); code != 0 {
		t.Fatalf("exit code = %d, want 0 (out: %s)", code, out.String())
	}
	recs := decodeRecords(t, out.String())
	if len(recs) != 1 || recs[0].Action != ActionDone {
		t.Fatalf("records = %+v, want one done", recs)
	}
}

func TestRunRejectsArtifactSHA256Mismatch(t *testing.T) {
	withConsent(t, "camp-hash", "read_frame")
	artifact := filepath.Join(t.TempDir(), "a.wasm")
	if err := os.WriteFile(artifact, []byte("actual bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	wrong := strings.Repeat("a", 64) // valid hex shape, wrong digest
	job := RunnerJob{
		CampaignID:     "camp-hash",
		ArtifactPath:   artifact,
		ArtifactSHA256: wrong,
		Grants:         map[string]bool{"read_frame": true},
	}
	in, _ := json.Marshal(job)

	var out bytes.Buffer
	if code := run(t.Context(), bytes.NewReader(in), &out); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	recs := decodeRecords(t, out.String())
	if len(recs) != 1 || recs[0].Action != ActionError || !strings.Contains(recs[0].Msg, "sha256") {
		t.Fatalf("records = %+v, want one sha256 mismatch error", recs)
	}
}

func TestRunRejectsTrailingData(t *testing.T) {
	artifact := filepath.Join(t.TempDir(), "a.wasm")
	_ = os.WriteFile(artifact, []byte("\x00asm"), 0o644)
	job, _ := json.Marshal(RunnerJob{CampaignID: "c", ArtifactPath: artifact, ArtifactSHA256: artifactSHA256(t, artifact)})

	var out bytes.Buffer
	if code := run(t.Context(), strings.NewReader(string(job)+"\n{}"), &out); code != 1 {
		t.Fatalf("exit code = %d, want 1 for trailing data", code)
	}
	recs := decodeRecords(t, out.String())
	if len(recs) != 1 || recs[0].Action != ActionError {
		t.Fatalf("records = %+v, want one error", recs)
	}
}
