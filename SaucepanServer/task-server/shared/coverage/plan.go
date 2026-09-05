// Package coverage implements the greedy multi-site continuous-coverage planner (#84/#397).
//
// Server-authoritative: researchers declare intent (pack/SDK); only the server
// selects sites. Weather (Open-Meteo) and reliability (reputation_stats) score
// factors are included alongside geometry + cohort.
package coverage

import (
	"math"
	"sort"
	"strings"
)

// Intent is optional campaign coverage configuration (default: disabled).
type Intent struct {
	Enabled             bool     `json:"enabled"`
	NMain               int      `json:"n_main"`
	Redundancy          bool     `json:"redundancy"`
	MaxGapMin           int      `json:"max_gap_min,omitempty"`
	MaxSites            int      `json:"max_sites,omitempty"`
	Mode                string   `json:"mode,omitempty"` // soft (default) | hard
	PreferredSites      []string `json:"preferred_sites,omitempty"`
	MinSites            int      `json:"min_sites,omitempty"`
	MinLongitudeSpanDeg float64  `json:"min_longitude_span_deg,omitempty"`
}

// DefaultIntent returns coverage off until explicitly enabled.
func DefaultIntent() Intent {
	return Intent{Enabled: false, NMain: 1, Redundancy: false, MaxGapMin: 45, MaxSites: 8, Mode: "soft"}
}

// Normalize fills defaults when enabled.
func (i Intent) Normalize() Intent {
	out := i
	if out.NMain <= 0 {
		out.NMain = 1
	}
	if out.MaxGapMin <= 0 {
		out.MaxGapMin = 45
	}
	if out.MaxSites <= 0 {
		out.MaxSites = 8
	}
	mode := strings.TrimSpace(strings.ToLower(out.Mode))
	if mode == "" {
		mode = "soft"
	}
	out.Mode = mode
	return out
}

// IsHard reports whether assign/apply should enforce gates without preferred fail-open.
func (i Intent) IsHard() bool {
	return strings.EqualFold(strings.TrimSpace(i.Mode), "hard")
}

// Factor scores a site for greedy selection (pluggable: geometry, cohort, weather, reliability).
type Factor interface {
	Name() string
	Score(site Site, targetRA, targetDec float64, already []Site) float64
}

// Plan is the greedy fill result (preview or apply).
type Plan struct {
	Primary           []Site   `json:"primary"`
	Redundant         []Site   `json:"redundant"`
	CoverageH         float64  `json:"estimated_coverage_hours"`
	MaxGapMin         float64  `json:"estimated_max_gap_min"`
	LongitudeSpanDeg  float64  `json:"longitude_span_deg,omitempty"`
	GateStatus        string   `json:"gate_status,omitempty"` // ok | degraded | failed
	GateReasons       []string `json:"gate_reasons,omitempty"`
	MeetsEstimatedGap *bool    `json:"meets_estimated_max_gap,omitempty"`
}

// GreedyFill picks NMain primaries then optional redundant sites with geographic spread.
// Does not mutate assignment — callers write handoff fields separately.
func GreedyFill(intent Intent, sites []Site, targetRA, targetDec float64, factors []Factor, allowEmulator bool) Plan {
	intent = intent.Normalize()
	plan := Plan{GateStatus: "ok"}
	if !intent.Enabled || len(sites) == 0 || len(factors) == 0 {
		plan.GateStatus = "insufficient_data"
		if intent.Enabled {
			plan.GateReasons = []string{"no candidate sites or factors"}
		}
		return plan
	}

	var pool []Site
	for _, s := range sites {
		if s.IsEmulator && !allowEmulator {
			continue
		}
		pool = append(pool, s)
	}

	// Hard mode with preferred_sites: restrict pool to preferred that are online.
	if intent.IsHard() && len(intent.PreferredSites) > 0 {
		pref := map[string]struct{}{}
		for _, id := range intent.PreferredSites {
			if id != "" {
				pref[id] = struct{}{}
			}
		}
		var restricted []Site
		for _, s := range pool {
			if _, ok := pref[s.TelescopeID]; ok {
				restricted = append(restricted, s)
			}
		}
		pool = restricted
	}

	pick := func(n int, already []Site) []Site {
		chosen := make([]Site, 0, n)
		remaining := append([]Site(nil), pool...)
		for len(chosen) < n && len(remaining) > 0 && len(already)+len(chosen) < intent.MaxSites {
			bestIdx := -1
			bestScore := math.Inf(-1)
			for i, s := range remaining {
				dup := false
				for _, a := range already {
					if a.TelescopeID == s.TelescopeID {
						dup = true
						break
					}
				}
				for _, c := range chosen {
					if c.TelescopeID == s.TelescopeID {
						dup = true
						break
					}
				}
				if dup {
					continue
				}
				score := 0.0
				ctx := append(append([]Site{}, already...), chosen...)
				for _, f := range factors {
					score += f.Score(s, targetRA, targetDec, ctx)
				}
				// Soft preferred bias (hard already restricted pool).
				if !intent.IsHard() {
					for _, id := range intent.PreferredSites {
						if id == s.TelescopeID {
							score += 2.0
							break
						}
					}
				}
				if score > bestScore {
					bestScore = score
					bestIdx = i
				}
			}
			if bestIdx < 0 {
				break
			}
			chosen = append(chosen, remaining[bestIdx])
			remaining = append(remaining[:bestIdx], remaining[bestIdx+1:]...)
		}
		return chosen
	}

	plan.Primary = pick(intent.NMain, nil)
	if intent.Redundancy {
		nRed := int(math.Ceil(0.5 * float64(intent.NMain)))
		if nRed < 1 {
			nRed = 1
		}
		plan.Redundant = pick(nRed, plan.Primary)
	}

	all := append(append([]Site{}, plan.Primary...), plan.Redundant...)
	if len(all) == 0 {
		plan.GateStatus = "failed"
		plan.GateReasons = []string{"no sites selected"}
		return EvaluateGates(intent, plan)
	}
	lons := make([]float64, 0, len(all))
	for _, s := range all {
		lons = append(lons, s.Lon)
	}
	plan.LongitudeSpanDeg = CircularLonSpanDeg(lons)
	span := plan.LongitudeSpanDeg
	plan.CoverageH = math.Min(24.0, 24.0*(span/360.0)*float64(len(all)))
	if plan.CoverageH < float64(len(all))*3 {
		plan.CoverageH = math.Min(24.0, float64(len(all))*3)
	}
	plan.MaxGapMin = math.Max(0, (24.0-plan.CoverageH)*60.0/float64(max(1, len(all))))
	meets := plan.MaxGapMin <= float64(intent.MaxGapMin)
	plan.MeetsEstimatedGap = &meets
	return EvaluateGates(intent, plan)
}

// EvaluateGates sets GateStatus/GateReasons from intent vs plan estimates.
func EvaluateGates(intent Intent, plan Plan) Plan {
	intent = intent.Normalize()
	reasons := append([]string{}, plan.GateReasons...)
	status := "ok"
	if !intent.Enabled {
		plan.GateStatus = "ok"
		plan.GateReasons = nil
		return plan
	}
	nSites := len(plan.Primary) + len(plan.Redundant)
	minSites := intent.MinSites
	if minSites <= 0 {
		minSites = intent.NMain
	}
	if nSites < minSites {
		reasons = append(reasons, "min_sites not met")
		status = worseGate(status, intent.IsHard())
	}
	if intent.MinLongitudeSpanDeg > 0 && plan.LongitudeSpanDeg+1e-9 < intent.MinLongitudeSpanDeg {
		reasons = append(reasons, "min_longitude_span_deg not met")
		status = worseGate(status, intent.IsHard())
	}
	if plan.MeetsEstimatedGap != nil && !*plan.MeetsEstimatedGap {
		reasons = append(reasons, "estimated_max_gap_min exceeds intent")
		status = worseGate(status, intent.IsHard())
	}
	if nSites == 0 {
		reasons = append(reasons, "no sites selected")
		status = "failed"
	}
	plan.GateStatus = status
	plan.GateReasons = uniqueStrings(reasons)
	return plan
}

func worseGate(cur string, hard bool) string {
	if hard {
		return "failed"
	}
	if cur == "failed" {
		return "failed"
	}
	return "degraded"
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// CircularLonSpanDeg returns the minimum arc (degrees) covering the longitudes.
func CircularLonSpanDeg(lons []float64) float64 {
	if len(lons) == 0 {
		return 0
	}
	if len(lons) == 1 {
		return 0
	}
	norm := make([]float64, len(lons))
	for i, lon := range lons {
		n := math.Mod(lon, 360)
		if n < 0 {
			n += 360
		}
		norm[i] = n
	}
	sort.Float64s(norm)
	maxGap := 0.0
	for i := 0; i < len(norm)-1; i++ {
		g := norm[i+1] - norm[i]
		if g > maxGap {
			maxGap = g
		}
	}
	wrap := (norm[0] + 360) - norm[len(norm)-1]
	if wrap > maxGap {
		maxGap = wrap
	}
	span := 360 - maxGap
	if span < 0 {
		return 0
	}
	return span
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// GeometryFactor prefers sites that extend longitude coverage (geo variance).
type GeometryFactor struct{}

func (GeometryFactor) Name() string { return "geometry" }

func (GeometryFactor) Score(site Site, _ float64, _ float64, already []Site) float64 {
	if len(already) == 0 {
		return 1.0
	}
	var sum float64
	for _, a := range already {
		sum += a.Lon
	}
	mean := sum / float64(len(already))
	d := math.Abs(site.Lon - mean)
	if d > 180 {
		d = 360 - d
	}
	return d / 180.0
}

// CohortFactor uses precomputed cohort compatibility (0..1).
type CohortFactor struct {
	Weight float64
}

func (CohortFactor) Name() string { return "cohort" }

func (f CohortFactor) Score(site Site, _ float64, _ float64, _ []Site) float64 {
	w := f.Weight
	if w <= 0 {
		w = 1.0
	}
	return site.CohortScore * w
}
