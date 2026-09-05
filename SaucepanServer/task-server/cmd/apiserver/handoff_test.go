package main

import (
	"testing"
	"time"

	"github.com/saucepan/hotpath/shared"
)

func TestParseOptionalTime(t *testing.T) {
	tests := []struct {
		name    string
		in      *string
		wantNil bool
		want    time.Time
	}{
		{"nil pointer", nil, true, time.Time{}},
		{"empty string", strPtr(""), true, time.Time{}},
		{"malformed", strPtr("not-a-time"), true, time.Time{}},
		{"valid RFC3339", strPtr("2026-01-02T03:04:05Z"), false, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)},
		{"valid RFC3339 with offset", strPtr("2026-01-02T03:04:05+02:00"), false, time.Date(2026, 1, 2, 1, 4, 5, 0, time.UTC)},
		{"date-only rejected (not RFC3339)", strPtr("2026-01-02"), true, time.Time{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseOptionalTime(tt.in)
			if tt.wantNil {
				if got != nil {
					t.Fatalf("parseOptionalTime(%v) = %v, want nil", strDeref(tt.in), got)
				}
				return
			}
			if got == nil {
				t.Fatalf("parseOptionalTime(%v) = nil, want %v", strDeref(tt.in), tt.want)
			}
			if !got.Equal(tt.want) {
				t.Fatalf("parseOptionalTime(%v) = %v, want %v", strDeref(tt.in), got.UTC(), tt.want)
			}
		})
	}
}

func strPtr(s string) *string { return &s }
func strDeref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func TestToSharedHandoffTaskMapsAllFields(t *testing.T) {
	ra := 10.5
	dec := -20.5
	minAlt := 30.0
	sched := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	userEnd := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	lead := 900
	emerg := time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)

	in := sharedHandoffTask{
		ID:                          7,
		Status:                      "in_progress",
		TargetRA:                    &ra,
		TargetDec:                   &dec,
		MinAltitudeDeg:              &minAlt,
		ScheduledEndAt:              &sched,
		UserEndAt:                   &userEnd,
		HandoffLeadSeconds:          &lead,
		EmergencyHandoffRequestedAt: &emerg,
	}
	out := toSharedHandoffTask(in)

	if out.ID != in.ID || out.Status != in.Status {
		t.Fatalf("scalar fields not mapped: %+v", out)
	}
	if out.TargetRA != in.TargetRA || out.TargetDec != in.TargetDec || out.MinAltitudeDeg != in.MinAltitudeDeg {
		t.Fatalf("pointer fields must be forwarded by identity: %+v", out)
	}
	if out.ScheduledEndAt != in.ScheduledEndAt || out.UserEndAt != in.UserEndAt {
		t.Fatalf("time pointer fields not forwarded: %+v", out)
	}
	if out.HandoffLeadSeconds != in.HandoffLeadSeconds {
		t.Fatalf("HandoffLeadSeconds not forwarded: %+v", out)
	}
	if out.EmergencyHandoffRequestedAt != in.EmergencyHandoffRequestedAt {
		t.Fatalf("EmergencyHandoffRequestedAt not forwarded: %+v", out)
	}
}

func TestToSharedHandoffTaskNilPointersPreserved(t *testing.T) {
	in := sharedHandoffTask{ID: 1, Status: "pending"}
	out := toSharedHandoffTask(in)
	if out.TargetRA != nil || out.TargetDec != nil || out.MinAltitudeDeg != nil ||
		out.ScheduledEndAt != nil || out.UserEndAt != nil || out.HandoffLeadSeconds != nil ||
		out.EmergencyHandoffRequestedAt != nil {
		t.Fatalf("expected all-nil optional fields to remain nil: %+v", out)
	}
}

func TestAnyHandoffBroadcastActiveEmpty(t *testing.T) {
	if anyHandoffBroadcastActive(nil, time.Now().UTC()) {
		t.Fatal("no tasks should never be active")
	}
	if anyHandoffBroadcastActive([]sharedHandoffTask{}, time.Now().UTC()) {
		t.Fatal("empty task slice should never be active")
	}
}

func TestAnyHandoffBroadcastActiveMirrorsSharedPackage(t *testing.T) {
	now := time.Now().UTC()
	soon := now.Add(1 * time.Minute)
	ra, dec := 1.0, 2.0
	tasks := []sharedHandoffTask{
		{ID: 1, Status: "in_progress", TargetRA: &ra, TargetDec: &dec, ScheduledEndAt: &soon},
	}
	apiActive := anyHandoffBroadcastActive(tasks, now)

	sharedTasks := []shared.HandoffTask{toSharedHandoffTask(tasks[0])}
	sharedActive := shared.AnyHandoffBroadcastActive(sharedTasks, now)

	if apiActive != sharedActive {
		t.Fatalf("apiserver wrapper (%v) diverged from shared.AnyHandoffBroadcastActive (%v)", apiActive, sharedActive)
	}
}

func TestBroadcastRecommendedPollSecondsMirrorsSharedPackage(t *testing.T) {
	now := time.Now().UTC()
	soon := now.Add(1 * time.Minute)
	ra, dec := 1.0, 2.0
	tasks := []sharedHandoffTask{
		{ID: 1, Status: "in_progress", TargetRA: &ra, TargetDec: &dec, ScheduledEndAt: &soon},
	}
	apiSeconds := broadcastRecommendedPollSeconds(tasks, now)
	sharedTasks := []shared.HandoffTask{toSharedHandoffTask(tasks[0])}
	sharedSeconds := shared.BroadcastRecommendedPollSeconds(sharedTasks, now)

	if apiSeconds != sharedSeconds {
		t.Fatalf("apiserver wrapper (%d) diverged from shared.BroadcastRecommendedPollSeconds (%d)", apiSeconds, sharedSeconds)
	}
}

func TestBuildHandoffStatusPayloadMirrorsSharedPackage(t *testing.T) {
	now := time.Now().UTC()
	ra, dec := 1.0, 2.0
	task := sharedHandoffTask{ID: 1, Status: "in_progress", TargetRA: &ra, TargetDec: &dec}
	safety := shared.TelescopeSafety{}

	apiPayload := buildHandoffStatusPayload(task, "telescope_1", safety, now, nil)
	sharedPayload := shared.BuildHandoffStatusPayload(toSharedHandoffTask(task), "telescope_1", safety, now, nil)

	if apiPayload != sharedPayload {
		t.Fatalf("apiserver wrapper payload diverged from shared package:\n got=%+v\nwant=%+v", apiPayload, sharedPayload)
	}
}
