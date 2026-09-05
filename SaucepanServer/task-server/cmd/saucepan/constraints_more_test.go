package main

import (
	"encoding/json"
	"testing"

	"github.com/saucepan/hotpath/shared/wire"
)

// TestApplyConstraintsValidation covers every rejection path in
// applyConstraints (constraints.go) — out-of-range power, non-positive
// max-exposure, out-of-range altitude limits, an inverted altitude range,
// and empty filters. A no-op call (nothing in cf.set) must always succeed.
func TestApplyConstraintsValidation(t *testing.T) {
	fresh := func() *wire.NodeMetadata { return &wire.NodeMetadata{Power: 0.5} }

	cases := []struct {
		name    string
		cf      constraintFlags
		wantErr bool
	}{
		{"no flags set is a no-op", constraintFlags{set: map[string]bool{}}, false},
		{"power below range", constraintFlags{power: -0.1, set: map[string]bool{"power": true}}, true},
		{"power above range", constraintFlags{power: 1.1, set: map[string]bool{"power": true}}, true},
		{"power at 0 boundary ok", constraintFlags{power: 0, set: map[string]bool{"power": true}}, false},
		{"power at 1 boundary ok", constraintFlags{power: 1, set: map[string]bool{"power": true}}, false},
		{"max-exposure zero rejected", constraintFlags{maxExposure: 0, set: map[string]bool{"max-exposure": true}}, true},
		{"max-exposure negative rejected", constraintFlags{maxExposure: -5, set: map[string]bool{"max-exposure": true}}, true},
		{"max-exposure positive ok", constraintFlags{maxExposure: 30, set: map[string]bool{"max-exposure": true}}, false},
		{"alt-min below -90 rejected", constraintFlags{altMin: -91, set: map[string]bool{"alt-min": true}}, true},
		{"alt-min above 90 rejected", constraintFlags{altMin: 91, set: map[string]bool{"alt-min": true}}, true},
		{"alt-max below -90 rejected", constraintFlags{altMax: -91, set: map[string]bool{"alt-max": true}}, true},
		{"alt-max above 90 rejected", constraintFlags{altMax: 91, set: map[string]bool{"alt-max": true}}, true},
		{"alt-min >= alt-max rejected", constraintFlags{altMin: 50, altMax: 50, set: map[string]bool{"alt-min": true, "alt-max": true}}, true},
		{"alt-min > alt-max rejected", constraintFlags{altMin: 60, altMax: 50, set: map[string]bool{"alt-min": true, "alt-max": true}}, true},
		{"alt-min < alt-max ok", constraintFlags{altMin: 10, altMax: 80, set: map[string]bool{"alt-min": true, "alt-max": true}}, false},
		{"filters empty after split rejected", constraintFlags{filters: " , ,", set: map[string]bool{"filters": true}}, true},
		{"filters empty string rejected", constraintFlags{filters: "", set: map[string]bool{"filters": true}}, true},
		{"filters valid ok", constraintFlags{filters: "L,R,G,B", set: map[string]bool{"filters": true}}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := fresh()
			err := applyConstraints(m, tc.cf)
			if (err != nil) != tc.wantErr {
				t.Fatalf("applyConstraints() err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

// TestApplyConstraintsAltMinOnlyAgainstExistingMax covers the case where
// only alt-min is set but a MountLimits.Altitude.Max already exists on the
// retained metadata — the min>=max guard must still fire using the existing
// max, not just newly-set values in the same call.
func TestApplyConstraintsAltMinOnlyAgainstExistingMax(t *testing.T) {
	existingMax := 40.0
	m := &wire.NodeMetadata{MountLimits: &wire.MountLimits{}}
	m.MountLimits.Altitude.Max = &existingMax

	cf := constraintFlags{altMin: 50, set: map[string]bool{"alt-min": true}}
	if err := applyConstraints(m, cf); err == nil {
		t.Fatal("alt-min above an existing alt-max should be rejected, got nil error")
	}
}

func TestSplitFilters(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"simple", "L,R,G,B", []string{"L", "R", "G", "B"}},
		{"whitespace trimmed", " L , R ,G,  B ", []string{"L", "R", "G", "B"}},
		{"empty string", "", nil},
		{"only commas", ",,,", nil},
		{"single", "Ha", []string{"Ha"}},
		{"trailing comma", "L,R,", []string{"L", "R"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitFilters(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("splitFilters(%q) = %v, want %v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("splitFilters(%q) = %v, want %v", tc.in, got, tc.want)
				}
			}
		})
	}
}

func TestHorizonPoints(t *testing.T) {
	if got := horizonPoints(nil); got != 0 {
		t.Fatalf("horizonPoints(nil) = %d, want 0", got)
	}
	var p wire.HorizonProfile
	if err := json.Unmarshal([]byte(`{"points":[{"az":0,"alt":1},{"az":90,"alt":2}],"interpolation":"linear"}`), &p); err != nil {
		t.Fatalf("seed unmarshal: %v", err)
	}
	if got := horizonPoints(&p); got != 2 {
		t.Fatalf("horizonPoints(2 points) = %d, want 2", got)
	}
	empty := &wire.HorizonProfile{}
	if got := horizonPoints(empty); got != 0 {
		t.Fatalf("horizonPoints(empty) = %d, want 0", got)
	}
}

func TestNumOrDash(t *testing.T) {
	if got := numOrDash(nil, "%.1f"); got != dash {
		t.Fatalf("numOrDash(nil) = %q, want dash %q", got, dash)
	}
	v := 3.14159
	if got := numOrDash(&v, "%.2f"); got != "3.14" {
		t.Fatalf("numOrDash(3.14159, %%.2f) = %q, want %q", got, "3.14")
	}
	zero := 0.0
	if got := numOrDash(&zero, "%.0f"); got != "0" {
		t.Fatalf("numOrDash(0) = %q, want %q", got, "0")
	}
}
