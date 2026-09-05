package coverage

import (
	"github.com/saucepan/hotpath/shared/weather"
)

// Site is a candidate telescope for continuous coverage.
type Site struct {
	TelescopeID string  `json:"telescope_id"`
	Lat         float64 `json:"lat"`
	Lon         float64 `json:"lon"`
	CohortScore float64 `json:"cohort_score"` // 0..1 compatibility with target requirements
	Reliability float64 `json:"reliability"`  // 0..1 from reputation_stats
	Weather     float64 `json:"weather"`      // 0..1 clearness (1 = clear)
	IsEmulator  bool    `json:"is_emulator"`
}

// WeatherFactor scores sites by current clearness (Open-Meteo cloud cover).
// Weight defaults to 1. Neutral 0.5 when weather unknown.
type WeatherFactor struct {
	Weight float64
}

func (WeatherFactor) Name() string { return "weather" }

func (f WeatherFactor) Score(site Site, _ float64, _ float64, _ []Site) float64 {
	w := f.Weight
	if w <= 0 {
		w = 1.0
	}
	score := site.Weather
	if score <= 0 {
		score = 0.5 // unknown → neutral (not zero — zero starved selection)
	}
	return score * w
}

// ReliabilityFactor scores sites by reputation_stats.reliability_score.
type ReliabilityFactor struct {
	Weight float64
}

func (ReliabilityFactor) Name() string { return "reliability" }

func (f ReliabilityFactor) Score(site Site, _ float64, _ float64, _ []Site) float64 {
	w := f.Weight
	if w <= 0 {
		w = 1.0
	}
	score := site.Reliability
	if score <= 0 {
		score = 0.5
	}
	return score * w
}

// EnrichWeather fills Site.Weather for each candidate (cached Open-Meteo).
func EnrichWeather(sites []Site) {
	for i := range sites {
		if sites[i].Lat == 0 && sites[i].Lon == 0 {
			sites[i].Weather = 0.5
			continue
		}
		snap := weather.Fetch(sites[i].Lat, sites[i].Lon)
		sites[i].Weather = snap.Clearness
		if sites[i].Weather <= 0 {
			sites[i].Weather = 0.5
		}
	}
}

// SiteIDs returns telescope IDs from a site slice.
func SiteIDs(sites []Site) []string {
	out := make([]string, 0, len(sites))
	for _, s := range sites {
		out = append(out, s.TelescopeID)
	}
	return out
}

// PreferredIDs returns primary then redundant IDs (deduped, order preserved).
func PreferredIDs(plan Plan) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range append(append([]Site{}, plan.Primary...), plan.Redundant...) {
		if s.TelescopeID == "" || seen[s.TelescopeID] {
			continue
		}
		seen[s.TelescopeID] = true
		out = append(out, s.TelescopeID)
	}
	return out
}

// DefaultFactors is the ship-gate factor set: geometry, cohort, weather, reliability.
func DefaultFactors() []Factor {
	return []Factor{
		GeometryFactor{},
		CohortFactor{Weight: 1},
		WeatherFactor{Weight: 1},
		ReliabilityFactor{Weight: 1},
	}
}
