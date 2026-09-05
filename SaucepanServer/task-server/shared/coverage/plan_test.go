package coverage

import "testing"

func TestGreedyFillDisabled(t *testing.T) {
	sites := []Site{{TelescopeID: "a", Lon: 0, CohortScore: 1}}
	plan := GreedyFill(DefaultIntent(), sites, 10, 40, []Factor{GeometryFactor{}, CohortFactor{}}, false)
	if len(plan.Primary) != 0 {
		t.Fatal("coverage off should select nothing")
	}
}

func TestGreedyFillNMainAndRedundancy(t *testing.T) {
	sites := []Site{
		{TelescopeID: "west", Lon: -120, CohortScore: 0.9},
		{TelescopeID: "mid", Lon: -60, CohortScore: 0.8},
		{TelescopeID: "east", Lon: 0, CohortScore: 0.7},
		{TelescopeID: "asia", Lon: 120, CohortScore: 0.6},
	}
	intent := Intent{Enabled: true, NMain: 2, Redundancy: true, MaxSites: 8}
	plan := GreedyFill(intent, sites, 10, 40, []Factor{GeometryFactor{}, CohortFactor{Weight: 1}}, false)
	if len(plan.Primary) != 2 {
		t.Fatalf("primary=%d want 2", len(plan.Primary))
	}
	if len(plan.Redundant) != 1 { // ceil(0.5*2)=1
		t.Fatalf("redundant=%d want 1", len(plan.Redundant))
	}
	ids := map[string]bool{}
	for _, s := range append(plan.Primary, plan.Redundant...) {
		if ids[s.TelescopeID] {
			t.Fatalf("duplicate site %s", s.TelescopeID)
		}
		ids[s.TelescopeID] = true
	}
}

func TestGreedySkipsEmulators(t *testing.T) {
	sites := []Site{
		{TelescopeID: "emu", Lon: 0, CohortScore: 1, IsEmulator: true},
		{TelescopeID: "real", Lon: 90, CohortScore: 0.5},
	}
	intent := Intent{Enabled: true, NMain: 2}
	plan := GreedyFill(intent, sites, 0, 0, []Factor{CohortFactor{}}, false)
	if len(plan.Primary) != 1 || plan.Primary[0].TelescopeID != "real" {
		t.Fatalf("unexpected primary: %+v", plan.Primary)
	}
}

func TestWeatherAndReliabilityPreferClearReliable(t *testing.T) {
	sites := []Site{
		{TelescopeID: "cloudy", Lon: 0, CohortScore: 1, Weather: 0.1, Reliability: 0.9},
		{TelescopeID: "clear", Lon: 90, CohortScore: 1, Weather: 0.95, Reliability: 0.9},
		{TelescopeID: "flaky", Lon: 180, CohortScore: 1, Weather: 0.95, Reliability: 0.1},
	}
	intent := Intent{Enabled: true, NMain: 1}
	plan := GreedyFill(intent, sites, 0, 0, []Factor{
		WeatherFactor{Weight: 2},
		ReliabilityFactor{Weight: 2},
	}, false)
	if len(plan.Primary) != 1 || plan.Primary[0].TelescopeID != "clear" {
		t.Fatalf("want clear site, got %+v", plan.Primary)
	}
}

func TestCircularLonSpanDateline(t *testing.T) {
	span := CircularLonSpanDeg([]float64{170, -170, 175})
	if span > 30 {
		t.Fatalf("dateline cluster span=%v want <30", span)
	}
}

func TestHardModePreferredRestrictsPool(t *testing.T) {
	sites := []Site{
		{TelescopeID: "west", Lon: -120, CohortScore: 1},
		{TelescopeID: "east", Lon: 0, CohortScore: 1},
	}
	intent := Intent{
		Enabled: true, NMain: 2, Mode: "hard",
		PreferredSites: []string{"west"},
		MinSites:       2,
	}
	plan := GreedyFill(intent, sites, 0, 0, []Factor{CohortFactor{}}, false)
	if len(plan.Primary)+len(plan.Redundant) != 1 {
		t.Fatalf("hard preferred should only pick west, got %+v", plan)
	}
	if plan.GateStatus != "failed" {
		t.Fatalf("want failed min_sites, got %s %v", plan.GateStatus, plan.GateReasons)
	}
}

func TestSoftModePreferredBiases(t *testing.T) {
	sites := []Site{
		{TelescopeID: "other", Lon: 0, CohortScore: 0.5},
		{TelescopeID: "pref", Lon: 90, CohortScore: 0.5},
	}
	intent := Intent{Enabled: true, NMain: 1, Mode: "soft", PreferredSites: []string{"pref"}}
	plan := GreedyFill(intent, sites, 0, 0, []Factor{CohortFactor{}}, false)
	if len(plan.Primary) != 1 || plan.Primary[0].TelescopeID != "pref" {
		t.Fatalf("want preferred bias, got %+v", plan.Primary)
	}
}
