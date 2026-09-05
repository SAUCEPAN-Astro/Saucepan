package shared

import (
	"testing"
	"time"
)

// cohort_filler_test.go — SelectCohort fan-out coverage promised by
// TASK_MATCHING.md Phase 7 / #407: fills to CohortMaxNodes, honours the
// per-node eligibility gates, returns the anchor first, degrades to N<max, and
// excludes a band-incompatible scope even when its raw idle score is good.

// similarScope is a gate-passing idle node with a fixed optics profile so every
// pair passes cohort.PassesBands (identical vectors → zero per-dim distance).
func similarScope(id string, startupMS int) NodeEvaluation {
	return NodeEvaluation{
		NodeID:             id,
		Status:             NodeStatusIdle,
		EstimatedStartupMS: startupMS,
		ReliabilityScore:   1.0,
		ApertureMM:         ptrF(200),
		FocalLengthMM:      ptrF(1000),
		PixelSizeUM:        ptrF(4.0),
		FOVWidthArcmin:     ptrF(60),
		FOVHeightArcmin:    ptrF(40),
		SiteSeeingArcsec:   ptrF(2.5),
		MountType:          intp(1), // equatorial
	}
}

func idsOf(rs []SelectorResult) map[string]bool {
	out := make(map[string]bool, len(rs))
	for _, r := range rs {
		out[r.NodeID] = true
	}
	return out
}

func TestSelectCohort_fillsToCohortMaxNodes(t *testing.T) {
	now := time.Date(2026, 6, 15, 2, 0, 0, 0, time.UTC)
	req := testReq(nil, nil, false)

	var nodes []NodeEvaluation
	for i := 0; i < CohortMaxNodes+4; i++ {
		nodes = append(nodes, similarScope(string(rune('a'+i)), 10+i))
	}

	got := SelectCohort(nodes, req, now)
	if len(got) != CohortMaxNodes {
		t.Fatalf("expected cohort capped at %d, got %d", CohortMaxNodes, len(got))
	}
	// Anchor is the lowest-idleScore node = smallest EstimatedStartupMS = "a".
	if got[0].NodeID != "a" {
		t.Fatalf("expected anchor 'a' first (primary-first order), got %q", got[0].NodeID)
	}
}

func TestSelectCohort_degradesBelowMax(t *testing.T) {
	now := time.Date(2026, 6, 15, 2, 0, 0, 0, time.UTC)
	req := testReq(nil, nil, false)

	nodes := []NodeEvaluation{
		similarScope("a", 10),
		similarScope("b", 20),
		similarScope("c", 30),
	}
	got := SelectCohort(nodes, req, now)
	if len(got) != 3 {
		t.Fatalf("3 eligible scopes → cohort of 3, got %d", len(got))
	}
	if got[0].NodeID != "a" {
		t.Fatalf("anchor should be 'a', got %q", got[0].NodeID)
	}
}

func TestSelectCohort_emptyWhenNoEligibleNode(t *testing.T) {
	now := time.Date(2026, 6, 15, 2, 0, 0, 0, time.UTC)
	req := testReq(nil, nil, false)

	if got := SelectCohort(nil, req, now); got != nil {
		t.Fatalf("nil fleet → nil, got %+v", got)
	}

	ineligible := []NodeEvaluation{
		{NodeID: "off", Status: NodeStatusOffline},
		{NodeID: "busy", Status: NodeStatusIdle, CurrentTaskID: intp(5)},
	}
	if got := SelectCohort(ineligible, req, now); got != nil {
		t.Fatalf("no eligible scope → nil, got %+v", got)
	}
}

func TestSelectCohort_honoursPerNodeGates(t *testing.T) {
	now := time.Date(2026, 6, 15, 2, 0, 0, 0, time.UTC)
	req := testReq(nil, nil, false)

	offline := similarScope("offline", 5)
	offline.Status = NodeStatusOffline
	emu := similarScope("emu_1", 5)
	emu.IsEmulator = true // production request → excluded
	busy := similarScope("busy", 5)
	busy.CurrentTaskID = intp(99)

	nodes := []NodeEvaluation{
		offline, emu, busy,
		similarScope("ok-1", 10),
		similarScope("ok-2", 20),
	}
	got := SelectCohort(nodes, req, now)
	ids := idsOf(got)
	if ids["offline"] || ids["emu_1"] || ids["busy"] {
		t.Fatalf("gated-out scope leaked into cohort: %+v", ids)
	}
	if len(got) != 2 || !ids["ok-1"] || !ids["ok-2"] {
		t.Fatalf("expected exactly {ok-1, ok-2}, got %+v", ids)
	}
}

func TestSelectCohort_excludesBandIncompatibleScope(t *testing.T) {
	now := time.Date(2026, 6, 15, 2, 0, 0, 0, time.UTC)
	req := testReq(nil, nil, false)

	anchor := similarScope("anchor", 0)
	simA := similarScope("sim-a", 10)
	simB := similarScope("sim-b", 10)

	// Alt-az mount (MountType 0) vs the equatorial anchor: the mount_type
	// vector dimension has a zero-width band, so PassesBands rejects it
	// forever — even though its idle score (startup 5) would rank it second.
	dissimilar := similarScope("dissimilar", 5)
	dissimilar.MountType = intp(0)

	got := SelectCohort([]NodeEvaluation{anchor, dissimilar, simA, simB}, req, now)
	ids := idsOf(got)
	if got[0].NodeID != "anchor" {
		t.Fatalf("anchor must stay the primary pick, got %q", got[0].NodeID)
	}
	if ids["dissimilar"] {
		t.Fatalf("band-incompatible scope must not join the cohort, got %+v", ids)
	}
	if !ids["sim-a"] || !ids["sim-b"] {
		t.Fatalf("band-compatible fillers missing, got %+v", ids)
	}
}
