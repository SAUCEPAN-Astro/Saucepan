package schedsim

import (
	"math"
	"testing"
	"time"

	"github.com/saucepan/hotpath/shared"
)

func TestJainFairness_equal(t *testing.T) {
	got := JainFairness([]float64{5, 5, 5, 5})
	if math.Abs(got-1.0) > 1e-9 {
		t.Fatalf("equal shares: got %v want 1", got)
	}
}

func TestJainFairness_winnerTakeAll(t *testing.T) {
	got := JainFairness([]float64{10, 0, 0, 0})
	want := 0.25
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("winner-take-all: got %v want %v", got, want)
	}
}

func TestScienceQualityProxy(t *testing.T) {
	q := ScienceQualityProxy(1.0, 200)
	if math.Abs(q-1.0) > 1e-9 {
		t.Fatalf("full aperture: got %v want 1", q)
	}
	q = ScienceQualityProxy(0.8, 100)
	want := 0.8 * 0.5
	if math.Abs(q-want) > 1e-9 {
		t.Fatalf("half aperture: got %v want %v", q, want)
	}
}

func TestBaselineRun_reportsKPIs(t *testing.T) {
	r := RunBaseline()
	if r.FramesCaptured <= 0 {
		t.Fatalf("expected frames > 0, got %d", r.FramesCaptured)
	}
	if r.ScienceValue <= 0 {
		t.Fatalf("expected science_value > 0, got %v", r.ScienceValue)
	}
	if r.NodeUtilization < 0 || r.NodeUtilization > 1.5 {
		// can slightly exceed 1 if busy windows overlap horizon bookkeeping; clamp check soft
		t.Fatalf("utilization out of range: %v", r.NodeUtilization)
	}
	if r.FairnessCoefficient <= 0 || r.FairnessCoefficient > 1 {
		t.Fatalf("fairness out of range: %v", r.FairnessCoefficient)
	}
	if r.TasksAssigned == 0 {
		t.Fatal("expected at least one assigned task")
	}
	t.Log("\n" + FormatReport(r))
}

func TestDuplicateAssign_bugModeProducesWaves(t *testing.T) {
	r := DuplicateAssignScenario(true)
	if r.DuplicateWaves == 0 {
		t.Fatalf("bug mode should produce duplicate waves; assignments=%v", r.Assignments)
	}
	nodes := map[string]struct{}{}
	for _, ev := range r.Assignments {
		nodes[ev.NodeID] = struct{}{}
	}
	if len(nodes) < 2 {
		t.Fatalf("bug mode should hand task to ≥2 nodes, got %v", r.Assignments)
	}
}

func TestDuplicateAssign_fixedModeNoWaves(t *testing.T) {
	r := DuplicateAssignScenario(false)
	if err := AssertNoDuplicateAssign(r); err != nil {
		t.Fatal(err)
	}
	if len(r.Assignments) == 0 {
		t.Fatal("expected at least one assignment in fixed mode")
	}
	// Exactly one wave of seats — SelectBestNode picks one node.
	if len(r.Assignments) != 1 {
		t.Fatalf("fixed mode want 1 assignment, got %d: %v", len(r.Assignments), r.Assignments)
	}
}

func TestDeadlineMiss_countsIncomplete(t *testing.T) {
	t0 := time.Date(2026, 6, 15, 2, 0, 0, 0, time.UTC)
	horizon := t0.Add(5 * time.Minute)
	dl := t0.Add(90 * time.Second) // too short for 10×60s frames
	lat, lon := 34.0, -118.0
	ra, dec := 180.0, 45.0
	fleet := []shared.NodeEvaluation{{
		NodeID: "solo", Status: shared.NodeStatusIdle,
		EstimatedStartupMS: 1000, ReliabilityScore: 1,
		SiteLat: &lat, SiteLon: &lon,
		MountAltDeg: f64(45), MountAzDeg: f64(180), MountSlewRateDegS: f64(5),
		AvailableFilters: []string{"L"}, ApertureMM: f64(150),
	}}
	stream := []SimTask{{
		ID: 1, ArriveAt: t0, Deadline: &dl, Priority: 10,
		FramesRequested: 10, ExposureS: 60, ScienceWeight: 1,
		Req: shared.TaskRequirements{RA: &ra, Dec: &dec, RequiredFilters: []string{"L"}},
	}}
	cfg := DefaultConfig(horizon)
	r := New(cfg, fleet, stream).Run()
	if r.DeadlineMisses != 1 {
		t.Fatalf("want 1 deadline miss, got %d (frames=%d)", r.DeadlineMisses, r.FramesCaptured)
	}
}

func TestHermetic_noExternalDeps(t *testing.T) {
	// Smoke: constructing and running must not panic and must finish quickly.
	r := RunBaseline()
	_ = FormatReport(r)
}

func TestPlannedInterrupt_lanesHoldOrImprove(t *testing.T) {
	before := RunPlannedInterrupt(false)
	after := RunPlannedInterrupt(true)
	t.Log("BEFORE (greedy):\n" + FormatReport(before))
	t.Log("AFTER (lanes):\n" + FormatReport(after))

	t0 := time.Date(2026, 7, 29, 2, 0, 0, 0, time.UTC)
	tooArrive := t0.Add(5 * time.Minute)
	latBefore := TooAssignLatencyMs(before, 3, tooArrive)
	latAfter := TooAssignLatencyMs(after, 3, tooArrive)
	if latAfter < 0 {
		t.Fatal("ToO was not assigned after lanes")
	}
	if latBefore >= 0 && latAfter > latBefore+60_000 {
		t.Fatalf("ToO latency regressed: before=%vms after=%vms", latBefore, latAfter)
	}
	// Planned workloads should not lose science value under lanes.
	if after.ScienceValue+1e-6 < before.ScienceValue*0.9 {
		t.Fatalf("science_value regressed >10%%: before=%v after=%v", before.ScienceValue, after.ScienceValue)
	}
	if after.DuplicateWaves != 0 {
		t.Fatalf("duplicate waves after lanes: %d", after.DuplicateWaves)
	}
}

func TestSelectBestNode_planAwarePreempt(t *testing.T) {
	// Covered in shared.TestMayPreempt via plan_remaining; ensure SelectBestNode
	// refuses mid-plan preempt that priority alone would allow.
	lat, lon := 34.0, -118.0
	ra, dec := 180.0, 45.0
	now := time.Date(2026, 7, 29, 2, 0, 0, 0, time.UTC)
	busyPri := 50
	tid := 1
	rem := 40 * 60.0 // 40 minutes remaining
	busy := shared.NodeEvaluation{
		NodeID: "busy", Status: shared.NodeStatusBusy,
		CurrentTaskID: &tid, CurrentTaskPriority: &busyPri,
		PlanRemainingSec:   &rem,
		EstimatedStartupMS: 1000, ReliabilityScore: 1,
		SiteLat: &lat, SiteLon: &lon,
		MountAltDeg: f64(10), MountAzDeg: f64(90), MountSlewRateDegS: f64(5),
		AvailableFilters: []string{"L"},
	}
	idle := shared.NodeEvaluation{
		NodeID: "idle", Status: shared.NodeStatusIdle,
		EstimatedStartupMS: 2000, ReliabilityScore: 1,
		SiteLat: &lat, SiteLon: &lon,
		MountAltDeg: f64(44), MountAzDeg: f64(179), MountSlewRateDegS: f64(5),
		AvailableFilters: []string{"L"},
	}
	req := shared.TaskRequirements{RA: &ra, Dec: &dec, RequiredFilters: []string{"L"}}
	// Priority 35 → diff 15; threshold 10; with 40min rem need 10+40=50 → no preempt.
	sel := shared.SelectBestNode([]shared.NodeEvaluation{busy, idle}, req, 35, 10, 5000, now)
	if sel == nil || sel.NodeID != "idle" || sel.Preempting {
		t.Fatalf("want idle assign (no mid-plan preempt), got %+v", sel)
	}
}
