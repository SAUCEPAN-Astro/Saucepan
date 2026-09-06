package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/saucepan/hotpath/internal/pierjob"
	"github.com/saucepan/hotpath/shared/wire"
)

func assignFixture() wire.AssignTaskPayload {
	return wire.AssignTaskPayload{
		TaskID:     7,
		CampaignID: "camp-x",
		PierCode:   &wire.PierCodeRef{SHA256: "abc"},
		PierCodeGrants: map[string]bool{
			wire.ActionBoardPost:   true,
			wire.ActionNextCapture: true,
		},
	}
}

func TestApplyBoardPostPublishesNote(t *testing.T) {
	var got wire.BoardNote
	pc := &pierCode{
		state:         map[string]json.RawMessage{},
		PostBoardNote: func(_, _ string, n wire.BoardNote) error { got = n; return nil },
	}
	a := assignFixture()
	rec := pierjob.Record{Action: wire.ActionBoardPost, Payload: json.RawMessage(`{"message":"tile 3 clear"}`)}

	if err := pc.apply("pier_a", a, a.PierCodeGrants, rec); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got.Message != "tile 3 clear" || got.NodeID != "pier_a" || got.CampaignID != "camp-x" || got.TaskID != "" || got.MessageID == "" {
		t.Fatalf("published note = %+v", got)
	}
}

func TestApplyBoardPostAllowsEmptyMessage(t *testing.T) {
	pc := &pierCode{state: map[string]json.RawMessage{}, PostBoardNote: func(_, _ string, _ wire.BoardNote) error { return nil }}
	a := assignFixture()
	rec := pierjob.Record{Action: wire.ActionBoardPost, Payload: json.RawMessage(`{"message":""}`)}
	if err := pc.apply("pier_a", a, a.PierCodeGrants, rec); err != nil {
		t.Fatalf("empty string is a valid opaque message: %v", err)
	}
}

func TestApplyRejectsUngrantedAction(t *testing.T) {
	pc := &pierCode{state: map[string]json.RawMessage{}}
	a := assignFixture()
	rec := pierjob.Record{Action: wire.ActionInboxAlert, Payload: json.RawMessage(`{}`)}
	if err := pc.apply("pier_a", a, a.PierCodeGrants, rec); err == nil {
		t.Fatal("want grant error for inbox_alert (not granted in fixture)")
	}
}

func TestApplyEventActionsPublishTypedBoardNotes(t *testing.T) {
	var got wire.BoardNote
	pc := &pierCode{
		state:         map[string]json.RawMessage{},
		PostBoardNote: func(_, _ string, n wire.BoardNote) error { got = n; return nil },
	}
	a := assignFixture()
	a.PierCodeGrants[wire.ActionRequestTime] = true

	rec := pierjob.Record{Action: wire.ActionRequestTime, Payload: json.RawMessage(`{"seconds":600}`)}
	if err := pc.apply("pier_a", a, a.PierCodeGrants, rec); err != nil {
		t.Fatalf("apply request_time: %v", err)
	}
	if got.EventType != "request_time" {
		t.Fatalf("event_type = %q, want request_time", got.EventType)
	}
	if string(got.Payload) != `{"seconds":600}` {
		t.Fatalf("payload = %s, want the raw record payload", got.Payload)
	}
	if got.NodeID != "pier_a" || got.CampaignID != "camp-x" {
		t.Fatalf("note addressing wrong: %+v", got)
	}
}

func TestPierBoardActionContract(t *testing.T) {
	cases := []struct {
		action string
		event  string
		body   string
	}{
		{wire.ActionBoardPost, "note", `{"message":"clear"}`},
		{wire.ActionInboxAlert, "alert", `{"message":"saturated"}`},
		{wire.ActionUrgencyFlag, "urgency", `{"message":"urgent"}`},
		{wire.ActionRequestTime, "request_time", `{"seconds":600}`},
	}
	for _, tc := range cases {
		t.Run(tc.action, func(t *testing.T) {
			var got wire.BoardNote
			pc := &pierCode{
				state:         map[string]json.RawMessage{},
				PostBoardNote: func(_, _ string, n wire.BoardNote) error { got = n; return nil },
			}
			a := assignFixture()
			a.PierCodeGrants[tc.action] = true
			if err := pc.apply("pier_a", a, a.PierCodeGrants, pierjob.Record{
				Action: tc.action, Payload: json.RawMessage(tc.body),
			}); err != nil {
				t.Fatalf("apply %s: %v", tc.action, err)
			}
			if got.CampaignID != "camp-x" || got.TaskID != "" || got.NodeID != "pier_a" || got.MessageID == "" {
				t.Fatalf("board addressing = %+v", got)
			}
			wantPayload := tc.body
			if tc.action == wire.ActionBoardPost {
				wantPayload = ""
			}
			if got.EventType != tc.event || string(got.Payload) != wantPayload {
				t.Fatalf("board event = type %q payload %s, want %q %s", got.EventType, got.Payload, tc.event, wantPayload)
			}
		})
	}
}

func TestBoardEventTypeMapping(t *testing.T) {
	cases := map[string]string{
		wire.ActionInboxAlert:  "alert",
		wire.ActionUrgencyFlag: "urgency",
		wire.ActionRequestTime: "request_time",
		wire.ActionBoardPost:   "note",
	}
	for action, want := range cases {
		if got := boardEventType(action); got != want {
			t.Errorf("boardEventType(%q) = %q, want %q", action, got, want)
		}
	}
}

func TestApplyNextCaptureStashesWithinBounds(t *testing.T) {
	pc := &pierCode{
		state: map[string]json.RawMessage{},
	}
	a := assignFixture()
	a.IntegrationTime = 120

	ok := pierjob.Record{Action: wire.ActionNextCapture, Payload: json.RawMessage(`{"exposure_sec":45}`)}
	if err := pc.apply("pier_a", a, a.PierCodeGrants, ok); err != nil {
		t.Fatalf("in-bounds next_capture: %v", err)
	}
	if p, err := pc.takePendingCapture(a.CampaignID, nextCaptureBounds(a), true); err != nil || p == nil || p.ExposureSec == nil || *p.ExposureSec != 45 {
		t.Fatalf("pending capture = %+v, want exposure 45", p)
	}
	if p, err := pc.takePendingCapture(a.CampaignID, nextCaptureBounds(a), true); err != nil || p != nil {
		t.Fatalf("pending capture should be consumed once, got %+v", p)
	}

	tooLong := pierjob.Record{Action: wire.ActionNextCapture, Payload: json.RawMessage(`{"exposure_sec":600}`)}
	if err := pc.apply("pier_a", a, a.PierCodeGrants, tooLong); err == nil {
		t.Fatal("want bounds error for 600s exposure")
	}
	if p, err := pc.takePendingCapture(a.CampaignID, nextCaptureBounds(a), true); err != nil || p != nil {
		t.Fatal("out-of-bounds next_capture must not stash")
	}
}

func TestApplyNextCaptureIsScopedToCampaign(t *testing.T) {
	pc := &pierCode{}
	a := assignFixture()
	a.IntegrationTime = 120
	b := a
	b.CampaignID = "other-campaign"
	rec := pierjob.Record{Action: wire.ActionNextCapture, Payload: json.RawMessage(`{"exposure_sec":45}`)}
	if err := pc.apply("pier_a", a, a.PierCodeGrants, rec); err != nil {
		t.Fatalf("campaign A next_capture: %v", err)
	}
	if got, err := pc.takePendingCapture(b.CampaignID, nextCaptureBounds(b), true); err != nil || got != nil {
		t.Fatalf("campaign B received campaign A override: %+v", got)
	}
	if got, err := pc.takePendingCapture(a.CampaignID, nextCaptureBounds(a), true); err != nil || got == nil {
		t.Fatal("campaign A override was not retained")
	}
}

func TestTakePendingCaptureRechecksCurrentAssignmentBounds(t *testing.T) {
	pc := &pierCode{}
	a := assignFixture()
	a.IntegrationTime = 120
	rec := pierjob.Record{Action: wire.ActionNextCapture, Payload: json.RawMessage(`{"exposure_sec":90}`)}
	if err := pc.apply("pier_a", a, a.PierCodeGrants, rec); err != nil {
		t.Fatalf("initial next_capture: %v", err)
	}

	narrow := a
	narrow.IntegrationTime = 60
	if got, err := pc.takePendingCapture(narrow.CampaignID, nextCaptureBounds(narrow), true); err == nil || got != nil {
		t.Fatalf("stale override = %+v, err=%v; want rejection", got, err)
	}
}

func TestTakePendingCaptureRequiresCurrentGrant(t *testing.T) {
	pc := &pierCode{}
	a := assignFixture()
	a.IntegrationTime = 120
	if err := pc.apply("pier_a", a, a.PierCodeGrants, pierjob.Record{
		Action:  wire.ActionNextCapture,
		Payload: json.RawMessage(`{"exposure_sec":45}`),
	}); err != nil {
		t.Fatalf("next_capture: %v", err)
	}

	if got, err := pc.takePendingCapture(a.CampaignID, nextCaptureBounds(a), false); err != nil || got != nil {
		t.Fatalf("revoked override = %+v, err=%v; want discard", got, err)
	}
	if got, err := pc.takePendingCapture(a.CampaignID, nextCaptureBounds(a), true); err != nil || got != nil {
		t.Fatalf("discarded override = %+v, err=%v; want nil", got, err)
	}
}

func TestApplyNextCaptureRejectsUndeclaredFilter(t *testing.T) {
	pc := &pierCode{}
	a := assignFixture()
	a.IntegrationTime = 60
	a.RequiredFilters = nil
	rec := pierjob.Record{Action: wire.ActionNextCapture, Payload: json.RawMessage(`{"filter":"R"}`)}
	if err := pc.apply("pier_a", a, a.PierCodeGrants, rec); err == nil {
		t.Fatal("filter override without a declared task filter must be rejected")
	}
}

func TestApplyTerminalRecords(t *testing.T) {
	pc := &pierCode{state: map[string]json.RawMessage{}}
	a := assignFixture()

	if err := pc.apply("p", a, a.PierCodeGrants, pierjob.Record{Action: pierjob.ActionDone, OK: true}); err != nil {
		t.Fatalf("done should not error: %v", err)
	}
	if err := pc.apply("p", a, a.PierCodeGrants, pierjob.Record{Action: pierjob.ActionError, Msg: "boom"}); err == nil {
		t.Fatal("error record should surface as an error")
	}
	st := pierjob.Record{Action: pierjob.ActionState, Payload: json.RawMessage(`{"seen":3}`)}
	if err := pc.apply("p", a, a.PierCodeGrants, st); err != nil {
		t.Fatalf("state: %v", err)
	}
	if string(pc.state["camp-x"]) != `{"seen":3}` {
		t.Fatalf("carried state = %s", pc.state["camp-x"])
	}
}

func TestParseRecords(t *testing.T) {
	out := "{\"action\":\"board_post\",\"payload\":{\"message\":\"a\"}}\n\n{\"action\":\"done\",\"ok\":true}\n"
	recs, err := parseRecords([]byte(out))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(recs) != 2 || recs[0].Action != "board_post" || recs[1].Action != "done" {
		t.Fatalf("recs = %+v", recs)
	}
	if _, err := parseRecords([]byte("{bad")); err == nil {
		t.Fatal("want error on malformed line")
	}
}

// TestRunForksRunnerAndAppliesRecords drives the whole run() path with a stub
// runner script that ignores stdin and prints canned records — no wasm needed
// to prove the fork/parse/apply wiring.
func TestRunForksRunnerAndAppliesRecords(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stub runner is a shell script")
	}
	dir := t.TempDir()
	stub := filepath.Join(dir, "stub-runner")
	script := "#!/bin/sh\n" +
		"cat >/dev/null\n" +
		`echo '{"action":"board_post","payload":{"message":"from stub"}}'` + "\n" +
		`echo '{"action":"done","ok":true}'` + "\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// A cached artifact so FetchVerifiedArtifact is a hit and never fetches.
	cache := filepath.Join(dir, "cache")
	if err := os.MkdirAll(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	body := []byte("x")
	digest := sha256.Sum256(body)
	sha := hex.EncodeToString(digest[:])
	if err := os.WriteFile(filepath.Join(cache, sha+".wasm"), body, 0o600); err != nil {
		t.Fatal(err)
	}

	var published int
	pc := &pierCode{
		RunnerPath:    stub,
		CacheDir:      cache,
		state:         map[string]json.RawMessage{},
		PostBoardNote: func(_, _ string, _ wire.BoardNote) error { published++; return nil },
	}
	a := assignFixture()
	a.PierCode = &wire.PierCodeRef{SHA256: sha}

	if err := pc.run(context.Background(), "pier_a", a, "", nil, nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	if published != 1 {
		t.Fatalf("board publishes = %d, want 1", published)
	}
}

func TestRunSkippedWhenDisabledOrUnconfigured(t *testing.T) {
	a := assignFixture()

	// feature off (no RunnerPath)
	if err := (&pierCode{}).run(context.Background(), "p", a, "", nil, nil); err != nil {
		t.Fatalf("unconfigured run should be a no-op: %v", err)
	}
	// kill switch
	a.PierCodeDisabled = true
	pc := &pierCode{RunnerPath: "/does/not/matter"}
	if err := pc.run(context.Background(), "p", a, "", nil, nil); err != nil {
		t.Fatalf("disabled run should be a no-op: %v", err)
	}
}
