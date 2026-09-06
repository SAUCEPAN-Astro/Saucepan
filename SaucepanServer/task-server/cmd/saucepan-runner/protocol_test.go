package main

import (
	"testing"

	"github.com/saucepan/hotpath/shared/wire"
)

// TestCheckRecordGrant is the #516 golden: an ungranted action is rejected,
// a granted one passes, an unknown action fails closed, and the runner's own
// terminal records always pass regardless of grants.
func TestCheckRecordGrant(t *testing.T) {
	grants := map[string]bool{
		wire.ActionReadFrame: true,
		wire.ActionBoardPost: true,
		wire.ActionBoardRead: false, // present but denied
	}

	cases := []struct {
		name    string
		action  string
		wantErr bool
	}{
		{"granted board_post", wire.ActionBoardPost, false},
		{"present-but-denied board_read", wire.ActionBoardRead, true},
		{"absent next_capture", wire.ActionNextCapture, true},
		{"absent inbox_alert", wire.ActionInboxAlert, true},
		{"unknown action name", "delete_everything", true},
		{"terminal done ignores grants", ActionDone, false},
		{"terminal error ignores grants", ActionError, false},
		{"terminal state ignores grants", ActionState, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkRecordGrant(RunnerRecord{Action: tc.action}, grants)
			if tc.wantErr && err == nil {
				t.Fatalf("action %q: want error, got nil", tc.action)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("action %q: want nil, got %v", tc.action, err)
			}
		})
	}
}

func TestCheckRecordGrantNilMapDeniesEverything(t *testing.T) {
	for _, a := range wire.PierCodeActions {
		if err := checkRecordGrant(RunnerRecord{Action: a}, nil); err == nil {
			t.Fatalf("nil grant map should deny %q", a)
		}
	}
	if err := checkRecordGrant(RunnerRecord{Action: ActionDone}, nil); err != nil {
		t.Fatalf("terminal done should still pass with nil grants: %v", err)
	}
}

func TestNextCaptureBoundsValidation(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	s := func(v string) *string { return &v }
	b := wire.NextCaptureBounds{
		MinExposureSec: 5, MaxExposureSec: 300,
		MinGain: 0, MaxGain: 100,
		AllowedFilters: []string{"R", "G", "B"},
	}

	if err := b.ValidateNextCapture(wire.NextCapturePayload{ExposureSec: f(120), Filter: s("R")}); err != nil {
		t.Fatalf("in-bounds payload rejected: %v", err)
	}
	if err := b.ValidateNextCapture(wire.NextCapturePayload{ExposureSec: f(600)}); err == nil {
		t.Fatal("exposure above campaign max should be rejected")
	}
	if err := b.ValidateNextCapture(wire.NextCapturePayload{ExposureSec: f(1)}); err == nil {
		t.Fatal("exposure below campaign min should be rejected")
	}
	if err := b.ValidateNextCapture(wire.NextCapturePayload{Filter: s("Ha")}); err == nil {
		t.Fatal("filter outside campaign set should be rejected")
	}
	if err := b.ValidateNextCapture(wire.NextCapturePayload{Gain: f(250)}); err == nil {
		t.Fatal("gain above campaign max should be rejected")
	}
}
