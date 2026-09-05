package main

import "testing"

func TestApplyTaskPatchZeroValuesAreNoOps(t *testing.T) {
	// applyTaskPatch treats a patch field's Go zero value as "not provided" —
	// there is no way to PATCH a numeric/string field back to zero/empty.
	dst := &Task{
		Name:            "original",
		Priority:        5,
		IntegrationTime: 100,
		MinAltitudeDeg:  30,
		ScienceBand:     "optical",
		RequiredFilters: []string{"r", "g"},
		AllowEmulator:   true,
	}
	patch := &Task{} // all zero values
	applyTaskPatch(dst, patch)

	if dst.Name != "original" || dst.Priority != 5 || dst.IntegrationTime != 100 ||
		dst.MinAltitudeDeg != 30 || dst.ScienceBand != "optical" || len(dst.RequiredFilters) != 2 {
		t.Fatalf("zero-value patch must leave dst unchanged, got %+v", dst)
	}
	if !dst.AllowEmulator {
		t.Fatal("AllowEmulator is OR-merged and must stay true once set")
	}
}

func TestApplyTaskPatchOverridesNonZeroFields(t *testing.T) {
	dst := &Task{
		Name:                         "original",
		Priority:                     5,
		IntegrationTime:              100,
		NormalizedIntegrationBudgetS: 200,
		MinPower:                     0.5,
		RequiredFilters:              []string{"r"},
		TargetRA:                     1,
		TargetDec:                    2,
		MinAltitudeDeg:               30,
		ScienceBand:                  "optical",
		MaxPSFFWHMArcsec:             3,
		MinPSFFWHMArcsec:             1,
		MinApertureMM:                50,
		MinSubExposureS:              10,
		MinResolutionArcsec:          1,
		MaxResolutionArcsec:          5,
		FOVWidthArcmin:               10,
		FOVHeightArcmin:              10,
	}
	patch := &Task{
		Name:                         "updated",
		Priority:                     9,
		IntegrationTime:              150,
		NormalizedIntegrationBudgetS: 250,
		MinPower:                     0.9,
		RequiredFilters:              []string{"i", "z"},
		TargetRA:                     3,
		TargetDec:                    4,
		MinAltitudeDeg:               45,
		ScienceBand:                  "infrared",
		MaxPSFFWHMArcsec:             4,
		MinPSFFWHMArcsec:             2,
		MinApertureMM:                80,
		MinSubExposureS:              20,
		MinResolutionArcsec:          2,
		MaxResolutionArcsec:          6,
		FOVWidthArcmin:               20,
		FOVHeightArcmin:              20,
	}
	applyTaskPatch(dst, patch)

	if dst.Name != "updated" || dst.Priority != 9 || dst.IntegrationTime != 150 ||
		dst.NormalizedIntegrationBudgetS != 250 || dst.MinPower != 0.9 ||
		len(dst.RequiredFilters) != 2 || dst.RequiredFilters[0] != "i" ||
		dst.TargetRA != 3 || dst.TargetDec != 4 || dst.MinAltitudeDeg != 45 ||
		dst.ScienceBand != "infrared" || dst.MaxPSFFWHMArcsec != 4 || dst.MinPSFFWHMArcsec != 2 ||
		dst.MinApertureMM != 80 || dst.MinSubExposureS != 20 ||
		dst.MinResolutionArcsec != 2 || dst.MaxResolutionArcsec != 6 ||
		dst.FOVWidthArcmin != 20 || dst.FOVHeightArcmin != 20 {
		t.Fatalf("expected all non-zero patch fields applied, got %+v", dst)
	}
}

func TestApplyTaskPatchAllowEmulatorIsOrNeverCleared(t *testing.T) {
	tests := []struct {
		name      string
		dstStart  bool
		patchFlag bool
		want      bool
	}{
		{"false + false = false", false, false, false},
		{"false + true = true", false, true, true},
		{"true + false stays true (cannot be cleared via patch)", true, false, true},
		{"true + true = true", true, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dst := &Task{AllowEmulator: tt.dstStart}
			patch := &Task{AllowEmulator: tt.patchFlag}
			applyTaskPatch(dst, patch)
			if dst.AllowEmulator != tt.want {
				t.Fatalf("AllowEmulator = %v, want %v", dst.AllowEmulator, tt.want)
			}
		})
	}
}

func TestApplyTaskPatchEmptyFilterSliceDoesNotClear(t *testing.T) {
	dst := &Task{RequiredFilters: []string{"r", "g"}}
	patch := &Task{RequiredFilters: []string{}} // len 0, not nil
	applyTaskPatch(dst, patch)
	if len(dst.RequiredFilters) != 2 {
		t.Fatalf("empty (non-nil) filter slice in patch must not clear existing filters, got %v", dst.RequiredFilters)
	}
}
