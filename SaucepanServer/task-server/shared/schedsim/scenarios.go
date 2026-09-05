package schedsim

import (
	"fmt"
	"time"

	"github.com/saucepan/hotpath/shared"
)

func f64(v float64) *float64 { return &v }

// BaselineFleet returns a small heterogeneous idle fleet for scoring PRs.
func BaselineFleet() []shared.NodeEvaluation {
	lat, lon := 34.0, -118.0
	return []shared.NodeEvaluation{
		{
			NodeID: "scope-premium-a", Status: shared.NodeStatusIdle,
			EstimatedStartupMS: 2000, QualityTier: "premium", ReliabilityScore: 0.95,
			SiteLat: &lat, SiteLon: &lon,
			MountAltDeg: f64(45), MountAzDeg: f64(180), MountSlewRateDegS: f64(5),
			AvailableFilters: []string{"R", "G", "B", "L"},
			ApertureMM:       f64(200), FocalLengthMM: f64(1000), PixelSizeUM: f64(3.76),
			FOVWidthArcmin: f64(60), FOVHeightArcmin: f64(40), SiteSeeingArcsec: f64(1.5),
			MaxStableExposureS: f64(300), IdleSinceMinutes: f64(10),
		},
		{
			NodeID: "scope-premium-b", Status: shared.NodeStatusIdle,
			EstimatedStartupMS: 2500, QualityTier: "premium", ReliabilityScore: 0.9,
			SiteLat: &lat, SiteLon: &lon,
			MountAltDeg: f64(40), MountAzDeg: f64(170), MountSlewRateDegS: f64(5),
			AvailableFilters: []string{"R", "G", "B", "L"},
			ApertureMM:       f64(180), FocalLengthMM: f64(900), PixelSizeUM: f64(3.76),
			FOVWidthArcmin: f64(55), FOVHeightArcmin: f64(38), SiteSeeingArcsec: f64(1.8),
			MaxStableExposureS: f64(240), IdleSinceMinutes: f64(5),
		},
		{
			NodeID: "scope-community-a", Status: shared.NodeStatusIdle,
			EstimatedStartupMS: 4000, QualityTier: "community", ReliabilityScore: 0.8,
			SiteLat: &lat, SiteLon: &lon,
			MountAltDeg: f64(30), MountAzDeg: f64(90), MountSlewRateDegS: f64(3),
			AvailableFilters: []string{"R", "G", "B"},
			ApertureMM:       f64(80), FocalLengthMM: f64(400), PixelSizeUM: f64(4.5),
			FOVWidthArcmin: f64(120), FOVHeightArcmin: f64(80), SiteSeeingArcsec: f64(2.5),
			MaxStableExposureS: f64(120), IdleSinceMinutes: f64(20),
		},
		{
			NodeID: "scope-community-b", Status: shared.NodeStatusIdle,
			EstimatedStartupMS: 4500, QualityTier: "community", ReliabilityScore: 0.75,
			SiteLat: &lat, SiteLon: &lon,
			MountAltDeg: f64(25), MountAzDeg: f64(100), MountSlewRateDegS: f64(3),
			AvailableFilters: []string{"R", "G", "B"},
			ApertureMM:       f64(70), FocalLengthMM: f64(350), PixelSizeUM: f64(4.5),
			FOVWidthArcmin: f64(130), FOVHeightArcmin: f64(90), SiteSeeingArcsec: f64(2.8),
			MaxStableExposureS: f64(90), IdleSinceMinutes: f64(30),
		},
		{
			NodeID: "scope-widefield", Status: shared.NodeStatusIdle,
			EstimatedStartupMS: 3000, QualityTier: "standard", ReliabilityScore: 0.85,
			SiteLat: &lat, SiteLon: &lon,
			MountAltDeg: f64(50), MountAzDeg: f64(200), MountSlewRateDegS: f64(4),
			AvailableFilters: []string{"L", "R", "G", "B", "Ha"},
			ApertureMM:       f64(51), FocalLengthMM: f64(250), PixelSizeUM: f64(4.63),
			FOVWidthArcmin: f64(240), FOVHeightArcmin: f64(160), SiteSeeingArcsec: f64(2.0),
			MaxStableExposureS: f64(180), IdleSinceMinutes: f64(15),
		},
	}
}

// BaselineStream returns a short task arrival stream over [t0, t0+horizon).
func BaselineStream(t0 time.Time) []SimTask {
	ra, dec := 83.822, -5.391
	dl1 := t0.Add(20 * time.Minute)
	dl2 := t0.Add(25 * time.Minute)
	dl3 := t0.Add(15 * time.Minute)
	return []SimTask{
		{
			ID: 1, Name: "betel-deep", ArriveAt: t0.Add(0), Deadline: &dl1,
			Priority: 50, FramesRequested: 10, ExposureS: 60, ScienceWeight: 2,
			Req: shared.TaskRequirements{
				RA: &ra, Dec: &dec, RequiredFilters: []string{"L"},
				MinApertureMM: f64(100),
			},
		},
		{
			ID: 2, Name: "betel-rgb", ArriveAt: t0.Add(2 * time.Minute), Deadline: &dl2,
			Priority: 40, FramesRequested: 6, ExposureS: 45, ScienceWeight: 1.5,
			Req: shared.TaskRequirements{
				RA: &ra, Dec: &dec, RequiredFilters: []string{"R", "G", "B"},
			},
		},
		{
			ID: 3, Name: "wide-survey", ArriveAt: t0.Add(5 * time.Minute), Deadline: &dl3,
			Priority: 30, FramesRequested: 8, ExposureS: 30, ScienceWeight: 1,
			Req: shared.TaskRequirements{
				RA: &ra, Dec: &dec, RequiredFilters: []string{"L"},
				RequiredFOVWidthArcmin: f64(100),
			},
		},
		{
			ID: 4, Name: "too-highpri", ArriveAt: t0.Add(8 * time.Minute), Deadline: &dl2,
			Priority: 90, FramesRequested: 4, ExposureS: 30, ScienceWeight: 3,
			Req: shared.TaskRequirements{
				RA: &ra, Dec: &dec, RequiredFilters: []string{"L"},
			},
		},
		{
			ID: 5, Name: "late-filler", ArriveAt: t0.Add(12 * time.Minute),
			Priority: 20, FramesRequested: 5, ExposureS: 30, ScienceWeight: 0.5,
			Req: shared.TaskRequirements{
				RA: &ra, Dec: &dec, RequiredFilters: []string{"R"},
			},
		},
	}
}

// RunBaseline scores the default fleet+stream (fixed / post-#400 behaviour).
func RunBaseline() Report {
	t0 := time.Date(2026, 6, 15, 2, 0, 0, 0, time.UTC)
	horizon := t0.Add(40 * time.Minute)
	cfg := DefaultConfig(horizon)
	return New(cfg, BaselineFleet(), BaselineStream(t0)).Run()
}

// DuplicateAssignScenario models the #400 bug path: one task, two idle nodes,
// assign then re-queue without gating on assigned state.
// When bugMode is true, expect DuplicateWaves > 0; when false, expect 0.
func DuplicateAssignScenario(bugMode bool) Report {
	t0 := time.Date(2026, 6, 15, 2, 0, 0, 0, time.UTC)
	horizon := t0.Add(10 * time.Minute)
	lat, lon := 34.0, -118.0
	ra, dec := 180.0, 45.0
	fleet := []shared.NodeEvaluation{
		{
			NodeID: "n1", Status: shared.NodeStatusIdle,
			EstimatedStartupMS: 1000, ReliabilityScore: 1.0,
			SiteLat: &lat, SiteLon: &lon,
			MountAltDeg: f64(44), MountAzDeg: f64(179), MountSlewRateDegS: f64(10),
			AvailableFilters: []string{"L"}, ApertureMM: f64(150),
			IdleSinceMinutes: f64(5),
		},
		{
			NodeID: "n2", Status: shared.NodeStatusIdle,
			EstimatedStartupMS: 2000, ReliabilityScore: 1.0,
			SiteLat: &lat, SiteLon: &lon,
			MountAltDeg: f64(10), MountAzDeg: f64(90), MountSlewRateDegS: f64(10),
			AvailableFilters: []string{"L"}, ApertureMM: f64(150),
			IdleSinceMinutes: f64(5),
		},
	}
	stream := []SimTask{{
		ID: 400, Name: "dup-assign-replay", ArriveAt: t0,
		Priority: 50, FramesRequested: 2, ExposureS: 60, ScienceWeight: 1,
		Req: shared.TaskRequirements{RA: &ra, Dec: &dec, RequiredFilters: []string{"L"}},
	}}
	cfg := DefaultConfig(horizon)
	cfg.BugMode400 = bugMode
	cfg.UseCohort = false // drain-style single SelectBestNode, matches #400 drainLoop path
	return New(cfg, fleet, stream).Run()
}

// AssertNoDuplicateAssign fails if any task received a second assign wave.
func AssertNoDuplicateAssign(r Report) error {
	if r.DuplicateWaves != 0 {
		return fmt.Errorf("#400 replay: want 0 duplicate assign waves, got %d", r.DuplicateWaves)
	}
	seen := map[int]map[string]struct{}{}
	for _, ev := range r.Assignments {
		if ev.Wave > 1 {
			return fmt.Errorf("#400 replay: task %d assigned again on wave %d to %s", ev.TaskID, ev.Wave, ev.NodeID)
		}
		if seen[ev.TaskID] == nil {
			seen[ev.TaskID] = map[string]struct{}{}
		}
		seen[ev.TaskID][ev.NodeID] = struct{}{}
	}
	return nil
}

// PlannedInterruptStream mixes long planned integrations with a late ToO (#421).
func PlannedInterruptStream(t0 time.Time) []SimTask {
	ra, dec := 83.822, -5.391
	dl := t0.Add(50 * time.Minute)
	return []SimTask{
		{
			ID: 1, Name: "planned-deep", ArriveAt: t0, Deadline: &dl,
			Priority: 30, FramesRequested: 20, ExposureS: 60, ScienceWeight: 2,
			Lane: "planned", PlannedStart: t0.Add(2 * time.Minute),
			Req: shared.TaskRequirements{RA: &ra, Dec: &dec, RequiredFilters: []string{"L"}, MinApertureMM: f64(100)},
		},
		{
			ID: 2, Name: "planned-rgb", ArriveAt: t0.Add(1 * time.Minute), Deadline: &dl,
			Priority: 25, FramesRequested: 12, ExposureS: 45, ScienceWeight: 1.5,
			Lane: "planned", PlannedStart: t0.Add(8 * time.Minute),
			Req: shared.TaskRequirements{RA: &ra, Dec: &dec, RequiredFilters: []string{"R"}},
		},
		{
			ID: 3, Name: "too-alert", ArriveAt: t0.Add(5 * time.Minute), Deadline: &dl,
			Priority: 90, FramesRequested: 4, ExposureS: 30, ScienceWeight: 4,
			Lane: "interrupt",
			Req:  shared.TaskRequirements{RA: &ra, Dec: &dec, RequiredFilters: []string{"L"}},
		},
	}
}

// RunPlannedInterrupt scores the #421 before/after harness.
// When useLanes is false, planned tasks assign greedily on arrival (legacy myopia).
// When true, planned tasks wait for PlannedStart and busy nodes carry PlanRemainingSec.
func RunPlannedInterrupt(useLanes bool) Report {
	t0 := time.Date(2026, 7, 29, 2, 0, 0, 0, time.UTC)
	horizon := t0.Add(55 * time.Minute)
	cfg := DefaultConfig(horizon)
	cfg.UseLanes = useLanes
	cfg.PreemptThreshold = 10
	return New(cfg, BaselineFleet(), PlannedInterruptStream(t0)).Run()
}

// TooAssignLatencyMs estimates ToO dispatch delay from arrival to first assign (proxy KPI).
func TooAssignLatencyMs(r Report, tooTaskID int, arriveAt time.Time) float64 {
	for _, ev := range r.Assignments {
		if ev.TaskID == tooTaskID {
			return float64(ev.At.Sub(arriveAt).Milliseconds())
		}
	}
	return -1
}
