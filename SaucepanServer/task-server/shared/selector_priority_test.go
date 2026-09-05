package shared

import (
	"testing"
	"time"
)

// selector_priority_test.go — the preemption-path coverage TASK_MATCHING.md
// promised for SelectBestNode (#407). SelectEligibleNodes / SelectCohort gate
// coverage lives in selector_test.go and cohort_filler_test.go.

// busyNode builds a gate-passing node already running a task at incumbentPri.
// No RA/Dec on the request → the slew estimate is 0 and only the priority
// (non-nearby) preemption branch can fire.
func busyNode(id string, incumbentTaskID, incumbentPri, startupMS int) NodeEvaluation {
	tid := incumbentTaskID
	pri := incumbentPri
	return NodeEvaluation{
		NodeID:              id,
		Status:              NodeStatusBusy,
		CurrentTaskID:       &tid,
		CurrentTaskPriority: &pri,
		EstimatedStartupMS:  startupMS,
		ReliabilityScore:    1.0,
	}
}

func TestSelectBestNode_preemptsOnlyAtOrAboveThreshold(t *testing.T) {
	now := time.Date(2026, 6, 15, 2, 0, 0, 0, time.UTC)
	req := testReq(nil, nil, false)

	// Incumbent priority 100, new task priority 90 → priorityDiff = 10.
	nodes := []NodeEvaluation{busyNode("busy-1", 777, 100, 0)}

	// threshold 10: 10 >= 10 → preempts.
	got := SelectBestNode(nodes, req, 90, 10, 0, now)
	if got == nil {
		t.Fatal("threshold 10, diff 10: expected a preemption result, got nil")
	}
	if !got.Preempting || got.Reason != "priority_preempt" {
		t.Fatalf("expected priority_preempt, got Preempting=%v Reason=%q", got.Preempting, got.Reason)
	}
	if got.PrevTaskID == nil || *got.PrevTaskID != 777 {
		t.Fatalf("PrevTaskID = %v, want 777 (the incumbent task)", got.PrevTaskID)
	}
	if got.IsIdle {
		t.Fatal("a preemption result must not be marked idle")
	}

	// threshold 11: 10 >= 11 is false → no eligible node.
	if got := SelectBestNode(nodes, req, 90, 11, 0, now); got != nil {
		t.Fatalf("threshold 11, diff 10: expected nil (below barrier), got %+v", got)
	}
}

func TestSelectBestNode_neverPreemptsEqualOrHigherPriorityIncumbent(t *testing.T) {
	now := time.Date(2026, 6, 15, 2, 0, 0, 0, time.UTC)
	req := testReq(nil, nil, false)

	// Equal priority: priorityDiff = 0 → MayPreempt false regardless of threshold.
	equal := []NodeEvaluation{busyNode("busy-eq", 1, 90, 0)}
	if got := SelectBestNode(equal, req, 90, 0, 0, now); got != nil {
		t.Fatalf("equal-priority incumbent must not be preempted, got %+v", got)
	}

	// Incumbent is *more* urgent than the new task (lower number = more urgent):
	// priorityDiff = 50 - 90 = -40 → never preempted.
	higher := []NodeEvaluation{busyNode("busy-hi", 2, 50, 0)}
	if got := SelectBestNode(higher, req, 90, 0, 0, now); got != nil {
		t.Fatalf("higher-priority incumbent must not be preempted, got %+v", got)
	}
}

func TestSelectBestNode_idleNodeBeatsPreemption(t *testing.T) {
	now := time.Date(2026, 6, 15, 2, 0, 0, 0, time.UTC)
	req := testReq(nil, nil, false)

	idle := NodeEvaluation{
		NodeID: "idle-1", Status: NodeStatusIdle,
		EstimatedStartupMS: 0, ReliabilityScore: 1.0,
	}
	busy := busyNode("busy-1", 9, 100, 5000) // expensive startup

	got := SelectBestNode([]NodeEvaluation{busy, idle}, req, 10, 1, 0, now)
	if got == nil {
		t.Fatal("expected the idle node, got nil")
	}
	if got.NodeID != "idle-1" || got.Preempting {
		t.Fatalf("expected idle-1 non-preempting, got NodeID=%q Preempting=%v Reason=%q",
			got.NodeID, got.Preempting, got.Reason)
	}
}

func TestSelectBestNode_noEligibleNodeReturnsNil(t *testing.T) {
	now := time.Date(2026, 6, 15, 2, 0, 0, 0, time.UTC)
	req := testReq(nil, nil, false)

	if got := SelectBestNode(nil, req, 10, 1, 0, now); got != nil {
		t.Fatalf("empty fleet: want nil, got %+v", got)
	}

	offline := []NodeEvaluation{
		{NodeID: "off-1", Status: NodeStatusOffline},
		{NodeID: "off-2", Status: NodeStatusOffline, CurrentTaskPriority: intp(50)},
	}
	if got := SelectBestNode(offline, req, 10, 1, 0, now); got != nil {
		t.Fatalf("all-offline fleet: want nil, got %+v", got)
	}

	// Emulator node but production request → pool isolation rejects it.
	emu := []NodeEvaluation{{NodeID: "emu_1", Status: NodeStatusIdle, IsEmulator: true}}
	if got := SelectBestNode(emu, req, 10, 1, 0, now); got != nil {
		t.Fatalf("emulator node in production pool: want nil, got %+v", got)
	}
}

func TestSelectBestNode_slewNearbyTiebreakBeatsFarPreemption(t *testing.T) {
	lat, lon := 34.0, -118.0
	ra, dec := 180.0, 45.0
	now := time.Date(2026, 6, 15, 2, 0, 0, 0, time.UTC)
	req := testReq(&ra, &dec, false)

	tAlt, tAz := ComputeTargetAltAz(ra, dec, lat, lon, now)

	// "near" incumbent: mount ~1° off target → a sub-second slew.
	nearAlt, nearAz := tAlt+0.5, tAz+0.7
	near := busyNode("near", 100, 80, 0)
	near.SiteLat, near.SiteLon = &lat, &lon
	near.MountAltDeg, near.MountAzDeg = &nearAlt, &nearAz
	near.MountSlewRateDegS = ptrF(5)

	// "far" incumbent: mount tens of degrees away → multi-second slew.
	farAlt, farAz := tAlt-35, tAz+110
	far := busyNode("far", 200, 80, 0)
	far.SiteLat, far.SiteLon = &lat, &lon
	far.MountAltDeg, far.MountAzDeg = &farAlt, &farAz
	far.MountSlewRateDegS = ptrF(5)

	// New task priority 70 → priorityDiff 10 for both, enough for either branch.
	// slewNearbyMs 1000: near (<1s) is "nearby", far (>1s) is not.
	got := SelectBestNode([]NodeEvaluation{far, near}, req, 70, 5, 1000, now)
	if got == nil {
		t.Fatal("expected a preemption result, got nil")
	}
	if got.NodeID != "near" {
		t.Fatalf("nearby incumbent should win the tiebreak, got %q (reason %q, score %d)",
			got.NodeID, got.Reason, got.Score)
	}
	if got.Reason != "nearby_preempt" || !got.IsNearby || !got.Preempting {
		t.Fatalf("expected nearby_preempt/IsNearby/Preempting, got Reason=%q IsNearby=%v Preempting=%v",
			got.Reason, got.IsNearby, got.Preempting)
	}
	if got.PrevTaskID == nil || *got.PrevTaskID != 100 {
		t.Fatalf("PrevTaskID = %v, want 100 (near incumbent's task)", got.PrevTaskID)
	}
}

func intp(v int) *int { return &v }
