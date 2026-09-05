package shared

import (
	"testing"
	"time"
)

func TestSelectBestNode_planAwareBlocksMidIntegration(t *testing.T) {
	lat, lon := 34.0, -118.0
	ra, dec := 180.0, 45.0
	now := time.Date(2026, 7, 29, 2, 0, 0, 0, time.UTC)
	busyPri := 50
	tid := 7
	rem := 40 * 60.0
	busy := NodeEvaluation{
		NodeID: "busy", Status: "observing",
		CurrentTaskID: &tid, CurrentTaskPriority: &busyPri,
		PlanRemainingSec:   &rem,
		EstimatedStartupMS: 1000, ReliabilityScore: 1,
		SiteLat: &lat, SiteLon: &lon,
		MountAltDeg: ptrF(10), MountAzDeg: ptrF(90), MountSlewRateDegS: ptrF(5),
		AvailableFilters: []string{"L"},
	}
	idle := NodeEvaluation{
		NodeID: "idle", Status: NodeStatusIdle,
		EstimatedStartupMS: 3000, ReliabilityScore: 1,
		SiteLat: &lat, SiteLon: &lon,
		MountAltDeg: ptrF(44), MountAzDeg: ptrF(179), MountSlewRateDegS: ptrF(5),
		AvailableFilters: []string{"L"},
	}
	req := TaskRequirements{RA: &ra, Dec: &dec, RequiredFilters: []string{"L"}}
	sel := SelectBestNode([]NodeEvaluation{busy, idle}, req, 35, 10, 5000, now)
	if sel == nil || sel.NodeID != "idle" || sel.Preempting {
		t.Fatalf("want idle (plan blocks preempt), got %+v", sel)
	}

	// Same priority gap with no plan remaining → preempt allowed.
	busy.PlanRemainingSec = nil
	sel = SelectBestNode([]NodeEvaluation{busy}, req, 35, 10, 5000, now)
	if sel == nil || !sel.Preempting {
		t.Fatalf("want preempt without plan cost, got %+v", sel)
	}
}

func TestMayPreemptHelpers(t *testing.T) {
	if PlanPreemptCostMs(10) != 10_000 {
		t.Fatal(PlanPreemptCostMs(10))
	}
	if EffectivePreemptThreshold(10, 125) != 12 { // +2 minutes
		t.Fatal(EffectivePreemptThreshold(10, 125))
	}
}
