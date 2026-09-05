package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saucepan/hotpath/shared/consent"
)

func TestCmdConsentApproveListRevoke(t *testing.T) {
	path := filepath.Join(t.TempDir(), "consent.json")

	// approve with explicit grants
	if code := cmdConsent([]string{"--file", path, "--approve", "camp-1", "--grants", "read_frame,board_post"}); code != exitOK {
		t.Fatalf("approve exit = %d", code)
	}
	st, err := consent.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := st.Campaigns["camp-1"]
	if !ok || len(rec.Actions) != 2 {
		t.Fatalf("camp-1 not recorded with 2 actions: %+v", st.Campaigns)
	}

	// list --json
	out := captureStdout(t, func() {
		if code := cmdConsent([]string{"--file", path, "--json"}); code != exitOK {
			t.Fatalf("list exit = %d", code)
		}
	})
	var rows []consentRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("list --json not valid: %v (%s)", err, out)
	}
	if len(rows) != 1 || rows[0].CampaignID != "camp-1" {
		t.Fatalf("rows = %+v", rows)
	}

	// revoke
	if code := cmdConsent([]string{"--file", path, "--revoke", "camp-1"}); code != exitOK {
		t.Fatalf("revoke exit = %d", code)
	}
	st, _ = consent.Load(path)
	if len(st.Campaigns) != 0 {
		t.Fatalf("camp-1 still present after revoke: %+v", st.Campaigns)
	}

	// revoke again → exitNoData
	if code := cmdConsent([]string{"--file", path, "--revoke", "camp-1"}); code != exitNoData {
		t.Fatalf("second revoke exit = %d, want %d", code, exitNoData)
	}

	// list empty → exitNoData
	if code := cmdConsent([]string{"--file", path}); code != exitNoData {
		t.Fatalf("empty list exit = %d, want %d", code, exitNoData)
	}
}

func TestCmdConsentApproveDefaultGrants(t *testing.T) {
	path := filepath.Join(t.TempDir(), "consent.json")
	if code := cmdConsent([]string{"--file", path, "--approve", "camp-x"}); code != exitOK {
		t.Fatalf("approve exit = %d", code)
	}
	st, _ := consent.Load(path)
	got := strings.Join(st.Campaigns["camp-x"].Actions, ",")
	if got != "board_post,board_read,read_frame" {
		t.Fatalf("default grants = %q", got)
	}
}

func TestCmdConsentRejectsUnknownActionAndBadFlags(t *testing.T) {
	path := filepath.Join(t.TempDir(), "consent.json")
	if code := cmdConsent([]string{"--file", path, "--approve", "c", "--grants", "read_frame,teleport"}); code != exitError {
		t.Fatalf("unknown action exit = %d, want %d", code, exitError)
	}
	if code := cmdConsent([]string{"--file", path, "--approve", "a", "--revoke", "b"}); code != exitError {
		t.Fatalf("approve+revoke exit = %d, want %d", code, exitError)
	}
}
