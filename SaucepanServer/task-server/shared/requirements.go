package shared

// TaskRequirements bundles everything the selector needs from a task/NotifyPayload.
type TaskRequirements struct {
	RA, Dec                 *float64
	MinAltitudeDeg          *float64
	MinPower                *float64
	TargetMagnitude         *float64
	RequiredFilters         []string
	AllowEmulator           bool
	MinApertureMM           *float64
	MinSubExposureS         *float64
	MinResolutionArcsec     *float64
	MaxResolutionArcsec     *float64
	MinPSFFWHMArcsec        *float64
	MaxPSFFWHMArcsec        *float64
	RequiredFOVWidthArcmin  *float64
	RequiredFOVHeightArcmin *float64
	ScienceBand             string
	CampaignID              string
	// PreferredNodeIDs — when set (coverage plan), selector prefers these sites.
	PreferredNodeIDs []string
	// PreferredFailOpen — soft coverage fail-open (default true). Hard mode sets false (#397).
	PreferredFailOpen bool
}

// RequirementsFromNotify maps a Postgres NOTIFY payload to selector requirements.
func RequirementsFromNotify(p NotifyPayload) TaskRequirements {
	preferred := append([]string{}, p.CoveragePrimary...)
	preferred = append(preferred, p.CoverageRedundant...)
	failOpen := !p.CoverageHardMode
	return TaskRequirements{
		RA:                      p.TargetRA,
		Dec:                     p.TargetDec,
		MinAltitudeDeg:          p.MinAltitudeDeg,
		MinPower:                p.MinPower,
		TargetMagnitude:         p.TargetMagnitude,
		RequiredFilters:         p.RequiredFilters,
		AllowEmulator:           p.AllowEmulator,
		MinApertureMM:           p.MinApertureMM,
		MinSubExposureS:         p.MinSubExposureS,
		MinResolutionArcsec:     p.MinResolutionArcsec,
		MaxResolutionArcsec:     p.MaxResolutionArcsec,
		MinPSFFWHMArcsec:        p.MinPSFFWHMArcsec,
		MaxPSFFWHMArcsec:        p.MaxPSFFWHMArcsec,
		RequiredFOVWidthArcmin:  p.RequiredFOVWidthArcmin,
		RequiredFOVHeightArcmin: p.RequiredFOVHeightArcmin,
		ScienceBand:             p.ScienceBand,
		CampaignID:              p.CampaignID,
		PreferredNodeIDs:        preferred,
		PreferredFailOpen:       failOpen,
	}
}
