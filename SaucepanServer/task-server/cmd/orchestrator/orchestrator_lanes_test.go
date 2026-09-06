package main

import (
	"context"
	"testing"
	"time"

	"github.com/saucepan/hotpath/shared"
	"github.com/saucepan/hotpath/shared/lanes"
	"go.uber.org/zap"
)

func TestSeasonInputsFromPayload(t *testing.T) {
	now := time.Now().UTC()
	winStart, winEnd := "2026-08-01T00:00:00Z", "2026-08-02T00:00:00Z"
	p := shared.NotifyPayload{
		SeasonKind:                  "too",
		SeasonUrgency:               "critical",
		SeasonCadenceGoalMin:        30,
		SeasonWindowStart:           &winStart,
		SeasonWindowEnd:             &winEnd,
		EmergencyHandoffRequestedAt: &now,
	}
	in := seasonInputsFromPayload(p)
	if in.Kind != "too" || in.Urgency != "critical" || in.CadenceGoalMin != 30 {
		t.Fatalf("seasonInputsFromPayload basic fields wrong: %+v", in)
	}
	if in.WindowStart == nil || *in.WindowStart != winStart {
		t.Fatalf("WindowStart = %v, want %s", in.WindowStart, winStart)
	}
	if !in.EmergencyHandoff {
		t.Fatal("EmergencyHandoff should be true when EmergencyHandoffRequestedAt is set")
	}

	// Zero-value payload: EmergencyHandoff must be false (nil pointer), not
	// panic on the presence check.
	zero := seasonInputsFromPayload(shared.NotifyPayload{})
	if zero.EmergencyHandoff {
		t.Fatal("EmergencyHandoff should be false for a payload with no emergency timestamp")
	}
	if zero.WindowStart != nil || zero.WindowEnd != nil {
		t.Fatalf("zero payload window fields should be nil: %+v", zero)
	}
}

func TestApplySeasonFromPack(t *testing.T) {
	// nil payload must not panic.
	applySeasonFromPack(nil, []byte(`{"season":{"kind":"too"}}`))

	// Empty packJSON is a no-op.
	p := shared.NotifyPayload{SeasonKind: "keep-me"}
	applySeasonFromPack(&p, nil)
	if p.SeasonKind != "keep-me" {
		t.Fatalf("empty packJSON should be a no-op, got SeasonKind=%q", p.SeasonKind)
	}

	// Malformed JSON is a no-op (ParsePack errors, caught and swallowed).
	p2 := shared.NotifyPayload{SeasonKind: "keep-me"}
	applySeasonFromPack(&p2, []byte(`not json`))
	if p2.SeasonKind != "keep-me" {
		t.Fatalf("malformed packJSON should be a no-op, got SeasonKind=%q", p2.SeasonKind)
	}

	// Pack with no season key is a no-op.
	p3 := shared.NotifyPayload{SeasonKind: "keep-me"}
	applySeasonFromPack(&p3, []byte(`{"targets":[]}`))
	if p3.SeasonKind != "keep-me" {
		t.Fatalf("pack with no season should be a no-op, got SeasonKind=%q", p3.SeasonKind)
	}

	// Valid season overwrites the season fields.
	p4 := shared.NotifyPayload{}
	applySeasonFromPack(&p4, []byte(`{"season":{"kind":"continuous","urgency":"normal","cadence_goal_min":45}}`))
	if p4.SeasonKind != "continuous" || p4.SeasonUrgency != "normal" || p4.SeasonCadenceGoalMin != 45 {
		t.Fatalf("valid season not applied: %+v", p4)
	}

	// Per-target cadence override only kicks in when season cadence is unset (<=0).
	p5 := shared.NotifyPayload{}
	applySeasonFromPack(&p5, []byte(`{"season":{"kind":"sparse"},"targets":[{"cadence_goal_min":10},{"cadence_goal_min":25}]}`))
	if p5.SeasonCadenceGoalMin != 25 {
		t.Fatalf("expected max target cadence override 25, got %d", p5.SeasonCadenceGoalMin)
	}

	// When season cadence IS set, target cadence must not override it.
	p6 := shared.NotifyPayload{}
	applySeasonFromPack(&p6, []byte(`{"season":{"kind":"sparse","cadence_goal_min":5},"targets":[{"cadence_goal_min":99}]}`))
	if p6.SeasonCadenceGoalMin != 5 {
		t.Fatalf("season cadence should not be overridden by target cadence, got %d", p6.SeasonCadenceGoalMin)
	}
}

func TestEnvDuration(t *testing.T) {
	key := "SP_TEST_ENVDURATION"
	if got := envDuration(key, 5*time.Second); got != 5*time.Second {
		t.Fatalf("unset envDuration = %v, want 5s", got)
	}
	t.Setenv(key, "2m")
	if got := envDuration(key, 5*time.Second); got != 2*time.Minute {
		t.Fatalf("envDuration(2m) = %v, want 2m", got)
	}
	t.Setenv(key, "not-a-duration")
	if got := envDuration(key, 5*time.Second); got != 5*time.Second {
		t.Fatalf("malformed envDuration should fall back, got %v", got)
	}
}

// TestEnqueueByLaneNilRedisNoPanic covers the guard clause: a nil Redis
// client (or non-positive task id) must short-circuit rather than dereference
// a nil pointer. This is the shape every entry point in this package calls
// enqueueByLane through when a Redis fetch already failed upstream.
func TestEnqueueByLaneNilRedisNoPanic(t *testing.T) {
	ctx := context.Background()
	enqueueByLane(ctx, nil, 5, 10, lanes.LaneInterrupt)
	enqueueByLane(ctx, nil, 0, 10, lanes.LaneInterrupt)
	enqueueByLane(ctx, nil, -1, 10, lanes.LanePlanned)
	// No assertion beyond "did not panic" — nil rdb has no observable Redis state.
}

// TestRunPlannerOnceNilRedisNoPanic: runPlannerOnce must be a safe no-op
// when rdb is nil, never reaching the pool at all.
func TestRunPlannerOnceNilRedisNoPanic(t *testing.T) {
	sugar := zap.NewNop().Sugar()
	if err := runPlannerOnce(context.Background(), nil, nil, sugar); err != nil {
		t.Fatalf("runPlannerOnce with nil rdb = %v, want nil error", err)
	}
}

// TestRefreshStandbyRostersNilRedisNoPanic mirrors the same nil-guard for
// the standby roster refresh path.
func TestRefreshStandbyRostersNilRedisNoPanic(t *testing.T) {
	sugar := zap.NewNop().Sugar()
	if err := refreshStandbyRosters(context.Background(), nil, sugar, time.Now()); err != nil {
		t.Fatalf("refreshStandbyRosters with nil rdb = %v, want nil error", err)
	}
}

// TestLoadStandbyRosterNilRedis: reading a roster with a nil client must
// return an empty roster, not panic — callers (assignTask) treat len==0 as
// "no roster, scan the full fleet."
func TestLoadStandbyRosterNilRedis(t *testing.T) {
	if got := loadStandbyRoster(context.Background(), nil, "too"); got != nil {
		t.Fatalf("loadStandbyRoster with nil rdb = %v, want nil", got)
	}
}

// TestAttachPlanRemainingNilRedis: with a nil Redis client, node evaluations
// must pass through completely unmodified (no PlanRemainingSec attached).
func TestAttachPlanRemainingNilRedis(t *testing.T) {
	nodes := []shared.NodeEvaluation{{NodeID: "n1"}, {NodeID: "n2"}}
	attachPlanRemaining(context.Background(), nil, nodes)
	for _, n := range nodes {
		if n.PlanRemainingSec != nil {
			t.Fatalf("node %s got PlanRemainingSec set with nil rdb: %v", n.NodeID, *n.PlanRemainingSec)
		}
	}
}

// TestAttachPlanRemainingSkipsAlreadySet: even with a nil rdb this is a
// no-op either way, but it documents the "continue if already set" guard —
// a node that already carries a value (e.g. attached earlier) is left alone.
func TestAttachPlanRemainingSkipsAlreadySet(t *testing.T) {
	v := 123.0
	nodes := []shared.NodeEvaluation{{NodeID: "n1", PlanRemainingSec: &v}}
	attachPlanRemaining(context.Background(), nil, nodes)
	if nodes[0].PlanRemainingSec == nil || *nodes[0].PlanRemainingSec != 123.0 {
		t.Fatalf("PlanRemainingSec changed unexpectedly: %v", nodes[0].PlanRemainingSec)
	}
}
