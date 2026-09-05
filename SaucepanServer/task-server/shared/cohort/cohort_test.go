package cohort

import "testing"

func TestPassesBands_rejectsMismatchedDimension(t *testing.T) {
	anchor := Vector{0.5, 0.5, 0.5, 0.5, 0}
	candidate := Vector{0.5, 0.5, 0.5, 0.9, 0}
	widths := [NDims]float64{0.30, 0.50, 0.50, 0.30, 0.0}
	if PassesBands(anchor, candidate, widths) {
		t.Fatal("expected reject on seeing dimension mismatch")
	}
}

func TestPassesBands_mountTypeExactMatch(t *testing.T) {
	anchor := Vector{0.5, 0.5, 0.5, 0.5, 0}
	candidate := Vector{0.5, 0.5, 0.5, 0.5, 1}
	widths := DefaultBandWidths
	if PassesBands(anchor, candidate, widths) {
		t.Fatal("expected reject when mount_type differs with zero width")
	}
}

func TestAdaptiveBandWidth_widensWhenSmallCohort(t *testing.T) {
	base := DefaultBandWidths
	wide := AdaptiveBandWidth(base, 1)
	if wide[0] <= base[0] {
		t.Fatalf("expected wider band for small cohort, base=%v wide=%v", base[0], wide[0])
	}
}

func TestAdaptiveBandWidth_tightensWhenLargeCohort(t *testing.T) {
	base := DefaultBandWidths
	tight := AdaptiveBandWidth(base, 10)
	if tight[0] >= base[0] {
		t.Fatalf("expected tighter band for large cohort, base=%v tight=%v", base[0], tight[0])
	}
}

func TestCohortHash_deterministic(t *testing.T) {
	ids := []string{"scope-a", "scope-b", "scope-c"}
	h1 := CohortHash(ids)
	h2 := CohortHash(ids)
	if h1 != h2 {
		t.Fatalf("expected deterministic hash, got %q then %q", h1, h2)
	}
	if h1 == "" {
		t.Fatal("expected non-empty hash for non-empty ids")
	}
}

func TestCohortHash_orderIndependent(t *testing.T) {
	a := CohortHash([]string{"scope-a", "scope-b", "scope-c"})
	b := CohortHash([]string{"scope-c", "scope-a", "scope-b"})
	if a != b {
		t.Fatalf("expected same hash regardless of input order, got %q vs %q", a, b)
	}
}

func TestCohortHash_distinctForDifferentSets(t *testing.T) {
	a := CohortHash([]string{"scope-a", "scope-b"})
	b := CohortHash([]string{"scope-a", "scope-c"})
	if a == b {
		t.Fatalf("expected different hashes for different ID sets, both got %q", a)
	}
}

func TestCohortHash_noFirstIDCollision(t *testing.T) {
	// Two different cohorts sharing a first-sorted ID must not collide
	// (this was the actual bug: the old impl returned ids[0]).
	a := CohortHash([]string{"scope-a", "scope-x"})
	b := CohortHash([]string{"scope-a", "scope-y"})
	if a == b {
		t.Fatalf("expected distinct hashes for cohorts sharing a first ID, both got %q", a)
	}
}

func TestCohortHash_emptySlice(t *testing.T) {
	if got := CohortHash(nil); got != "" {
		t.Fatalf("expected empty string for nil ids, got %q", got)
	}
	if got := CohortHash([]string{}); got != "" {
		t.Fatalf("expected empty string for empty ids, got %q", got)
	}
}
