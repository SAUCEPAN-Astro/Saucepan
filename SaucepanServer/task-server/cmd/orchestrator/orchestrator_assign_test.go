package main

import (
	"testing"
	"time"

	"github.com/saucepan/hotpath/shared"
	"go.uber.org/zap"
)

func TestRedisZMemberInt(t *testing.T) {
	cases := []struct {
		name    string
		member  interface{}
		want    int
		wantErr bool
	}{
		{"float64", float64(42), 42, false},
		{"int64", int64(7), 7, false},
		{"int", int(3), 3, false},
		{"string digits", "99", 99, false},
		{"string non-numeric", "abc", 0, true},
		{"negative float64", float64(-5), -5, false},
		{"bool unexpected type", true, 0, true},
		{"nil unexpected type", nil, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := redisZMemberInt(tc.member)
			if (err != nil) != tc.wantErr {
				t.Fatalf("redisZMemberInt(%v) err = %v, wantErr %v", tc.member, err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Fatalf("redisZMemberInt(%v) = %d, want %d", tc.member, got, tc.want)
			}
		})
	}
}

func TestFindNodeEval(t *testing.T) {
	nodes := []shared.NodeEvaluation{
		{NodeID: "n1", Status: "idle"},
		{NodeID: "n2", Status: "busy"},
	}
	if got := findNodeEval(nodes, "n2"); got == nil || got.NodeID != "n2" {
		t.Fatalf("findNodeEval(n2) = %+v, want node n2", got)
	}
	if got := findNodeEval(nodes, "ghost"); got != nil {
		t.Fatalf("findNodeEval(ghost) = %+v, want nil", got)
	}
	if got := findNodeEval(nil, "n1"); got != nil {
		t.Fatalf("findNodeEval(nil slice) = %+v, want nil", got)
	}
	if got := findNodeEval([]shared.NodeEvaluation{}, "n1"); got != nil {
		t.Fatalf("findNodeEval(empty slice) = %+v, want nil", got)
	}
}

func TestSafeFloat64(t *testing.T) {
	if got := safeFloat64(nil); got != 0 {
		t.Fatalf("safeFloat64(nil) = %v, want 0", got)
	}
	v := 3.5
	if got := safeFloat64(&v); got != 3.5 {
		t.Fatalf("safeFloat64(&3.5) = %v, want 3.5", got)
	}
	zero := 0.0
	if got := safeFloat64(&zero); got != 0 {
		t.Fatalf("safeFloat64(&0.0) = %v, want 0", got)
	}
}

func TestFilterNodesByCampaign(t *testing.T) {
	nodes := []shared.NodeEvaluation{
		{NodeID: "n1", EnabledCampaignIDs: []string{"camp-a"}},
		{NodeID: "n2", EnabledCampaignIDs: []string{"camp-b"}},
		{NodeID: "n3", EnabledCampaignIDs: nil}, // opted into nothing
	}

	// Standalone task (empty campaignID) — every node passes through.
	if got := filterNodesByCampaign(nodes, ""); len(got) != 3 {
		t.Fatalf("filterNodesByCampaign(\"\") = %d nodes, want 3", len(got))
	}

	// Only n1 serves camp-a.
	got := filterNodesByCampaign(nodes, "camp-a")
	if len(got) != 1 || got[0].NodeID != "n1" {
		t.Fatalf("filterNodesByCampaign(camp-a) = %+v, want only n1", got)
	}

	// No node opted into an unknown campaign.
	if got := filterNodesByCampaign(nodes, "camp-unknown"); len(got) != 0 {
		t.Fatalf("filterNodesByCampaign(camp-unknown) = %+v, want empty", got)
	}

	// Nil input never panics.
	if got := filterNodesByCampaign(nil, "camp-a"); len(got) != 0 {
		t.Fatalf("filterNodesByCampaign(nil) = %+v, want empty", got)
	}
}

func TestAssignPayloadFromNotify(t *testing.T) {
	ra, dec := 180.0, 45.0
	integ := 120.0
	minAlt := 20.0
	payload := shared.NotifyPayload{
		TaskID:          7,
		CampaignID:      "camp-1",
		Priority:        50,
		Name:            "test-task",
		TargetRA:        &ra,
		TargetDec:       &dec,
		IntegrationTime: &integ,
		RequiredFilters: []string{"L", "R"},
		MinAltitudeDeg:  &minAlt,
	}

	out := assignPayloadFromNotify(payload)
	if out.TaskID != 7 || out.CampaignID != "camp-1" || out.Priority != 50 || out.Name != "test-task" {
		t.Fatalf("assignPayloadFromNotify basic fields wrong: %+v", out)
	}
	if out.IntegrationTime != 120.0 {
		t.Fatalf("IntegrationTime = %v, want 120.0", out.IntegrationTime)
	}
	if len(out.RequiredFilters) != 2 {
		t.Fatalf("RequiredFilters = %v, want 2 entries", out.RequiredFilters)
	}
	// No handoff fields set — urgency none, so HandoffUrgency stays empty.
	if out.HandoffUrgency != "" {
		t.Fatalf("HandoffUrgency = %q, want empty for a task with no handoff fields", out.HandoffUrgency)
	}
	if out.ScheduledEndAt != nil {
		t.Fatalf("ScheduledEndAt = %v, want nil (not set on payload)", out.ScheduledEndAt)
	}

	// nil IntegrationTime must not panic and must round-trip to 0.
	payloadNilInteg := shared.NotifyPayload{TaskID: 8}
	out2 := assignPayloadFromNotify(payloadNilInteg)
	if out2.IntegrationTime != 0 {
		t.Fatalf("IntegrationTime with nil pointer = %v, want 0", out2.IntegrationTime)
	}

	// Emergency handoff requested — HandoffUrgency must be "emergency" and
	// ScheduledEndAt formatted as RFC3339 when set.
	now := time.Now().UTC()
	schedEnd := now.Add(time.Hour)
	payloadEmergency := shared.NotifyPayload{
		TaskID:                      9,
		EmergencyHandoffRequestedAt: &now,
		ScheduledEndAt:              &schedEnd,
	}
	out3 := assignPayloadFromNotify(payloadEmergency)
	if out3.HandoffUrgency != string(shared.UrgencyEmergency) {
		t.Fatalf("HandoffUrgency = %q, want %q", out3.HandoffUrgency, shared.UrgencyEmergency)
	}
	if out3.ScheduledEndAt == nil || *out3.ScheduledEndAt != schedEnd.UTC().Format(time.RFC3339) {
		t.Fatalf("ScheduledEndAt = %v, want formatted %s", out3.ScheduledEndAt, schedEnd.UTC().Format(time.RFC3339))
	}

	// On-pier code fields ride through untouched (#516 grants / #518 artifact
	// ref / #520 kill switch). An ordinary payload leaves them zero.
	if out.PierCodeGrants != nil || out.PierCode != nil || out.PierCodeDisabled {
		t.Fatalf("ordinary payload set pier_code fields: %+v", out)
	}
	hash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	payloadCode := shared.NotifyPayload{
		TaskID:           10,
		PierCodeGrants:   map[string]bool{"read_frame": true},
		PierCode:         &shared.PierCodeRef{SHA256: hash, URL: "https://example.test/a.wasm"},
		PierCodeDisabled: true,
	}
	out4 := assignPayloadFromNotify(payloadCode)
	if !out4.PierCodeGrants["read_frame"] || !out4.PierCodeDisabled {
		t.Fatalf("pier_code grant/kill-switch not carried: %+v", out4)
	}
	if out4.PierCode == nil || out4.PierCode.SHA256 != hash {
		t.Fatalf("pier_code artifact ref not carried: %+v", out4.PierCode)
	}
}

// TestEmitAssignmentEvent exercises the metrics-shaping path with a
// MetricsCollector backed by a nil Redis client (Emit only logs + buffers
// in-process; both refreshGauges() and Flush() nil-check m.rdb) so this runs
// with no live infra, matching this scope's fake/mock convention.
//
// Stop()/Flush() with a nil rdb and buffered events used to panic on
// m.rdb.Context(); #484 added the `m.rdb != nil` guard to Flush(), so the
// deferred Stop() below now also covers that path.
func TestEmitAssignmentEvent(t *testing.T) {
	sugar := zap.NewNop().Sugar()
	metrics := shared.NewMetricsCollector(nil, sugar, 0)
	defer metrics.Stop()

	ra, dec := 10.0, 20.0
	payload := shared.NotifyPayload{TaskID: 1, Priority: 5, CampaignID: "camp-x", TargetRA: &ra, TargetDec: &dec}
	slewRate := 2.5
	nodes := []shared.NodeEvaluation{
		{NodeID: "n1", QualityTier: "gold", MountSlewRateDegS: &slewRate, AvailableFilters: []string{"L"}},
	}
	sel := shared.SelectorResult{NodeID: "n1", SlewTimeMs: 500, Reason: "best_fit"}

	// Must not panic with a fully populated node list and a normal assign.
	emitAssignmentEvent(metrics, payload, sel, nodes, 1000, 5.0, 2.0, nil)

	// Must not panic when the selected node isn't in the evaluation list
	// (selNode == nil path) or when the timer is nil.
	emitAssignmentEvent(metrics, payload, sel, nil, 1000, 5.0, 2.0, nil)

	// Preemption path: PrevTaskID set but selNode nil — must not deref a nil
	// CurrentTaskPriority.
	prevID := 42
	selPreempt := shared.SelectorResult{NodeID: "ghost", Preempting: true, PrevTaskID: &prevID}
	emitAssignmentEvent(metrics, payload, selPreempt, nodes, 1000, 5.0, 2.0, nil)
}
