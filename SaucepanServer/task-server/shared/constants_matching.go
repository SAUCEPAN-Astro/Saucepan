package shared

const (
	// FOVHeadroomFactor — scope FOV must be >= required FOV * this factor.
	FOVHeadroomFactor = 1.2

	// DiffractionLimitConstant — Dawes-limit approximation: arcsec = constant / aperture_mm.
	DiffractionLimitConstant = 116.0

	// IdleSaturationMinutes — scarcity protection fully decays after this idle duration.
	IdleSaturationMinutes = 45.0

	// ScarcityPenaltyStrength — filler-seat score multiplier for scarce, fresh-idle scopes.
	ScarcityPenaltyStrength = 1.5

	// CohortCosineBonusWeight — ranking bonus for cosine similarity among band-passing fillers.
	CohortCosineBonusWeight = 500.0
)
