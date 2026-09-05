package consent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingFileIsEmptyStore(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("Load missing: %v", err)
	}
	if len(s.Campaigns) != 0 || s.Version != FileVersion {
		t.Fatalf("want empty v%d store, got %+v", FileVersion, s)
	}
}

func TestApproveSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	s, _ := Load(path)
	s.Approve("camp-1", []string{"board_post", "read_frame", "board_post", ""}) // dupes + empty
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}

	back, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := back.Campaigns["camp-1"]
	if !ok {
		t.Fatal("camp-1 not persisted")
	}
	if len(rec.Actions) != 2 || rec.Actions[0] != "board_post" || rec.Actions[1] != "read_frame" {
		t.Fatalf("actions not normalized/sorted: %v", rec.Actions)
	}
	if rec.ApprovedAt.IsZero() {
		t.Fatal("approved_at not set")
	}

	// File is 0600.
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("consent file perm = %o, want 600", info.Mode().Perm())
	}
}

func TestAllowsGate(t *testing.T) {
	s := &Store{Campaigns: map[string]Record{}}
	s.Approve("camp-1", []string{"read_frame", "board_post"})

	if ok, _ := s.Allows("camp-unknown", nil); ok {
		t.Fatal("unknown campaign must be denied")
	}
	if ok, reason := s.Allows("camp-1", []string{"read_frame"}); !ok {
		t.Fatalf("subset should be allowed: %s", reason)
	}
	if ok, reason := s.Allows("camp-1", []string{"read_frame", "board_post"}); !ok {
		t.Fatalf("exact set should be allowed: %s", reason)
	}
	if ok, _ := s.Allows("camp-1", []string{"read_frame", "next_capture"}); ok {
		t.Fatal("a not-consented action must be denied")
	}
	if ok, _ := s.Allows("camp-1", nil); !ok {
		t.Fatal("empty want should check approval only and pass")
	}
}

func TestRevoke(t *testing.T) {
	s := &Store{Campaigns: map[string]Record{}}
	s.Approve("camp-1", []string{"read_frame"})
	if !s.Revoke("camp-1") {
		t.Fatal("revoke of an approved campaign should return true")
	}
	if s.Revoke("camp-1") {
		t.Fatal("second revoke should return false")
	}
	if ok, _ := s.Allows("camp-1", nil); ok {
		t.Fatal("revoked campaign must be denied")
	}
}

func TestDefaultPathHonorsEnvOverride(t *testing.T) {
	t.Setenv(EnvOverride, "/custom/consent.json")
	p, err := DefaultPath()
	if err != nil || p != "/custom/consent.json" {
		t.Fatalf("DefaultPath = %q, %v", p, err)
	}
}
