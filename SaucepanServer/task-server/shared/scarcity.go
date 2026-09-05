package shared

// IdleBonus — 0 when just-idle, ramps to 1.0 once idle for IdleSaturationMinutes.
func IdleBonus(idleMinutes float64) float64 {
	if idleMinutes <= 0 {
		return 0
	}
	b := idleMinutes / IdleSaturationMinutes
	if b > 1.0 {
		return 1.0
	}
	return b
}

// ScarcityScore — high (near 1.0) when few/no idle substitutes exist right now.
func ScarcityScore(substitutesIdle int) float64 {
	return 1.0 / float64(1+substitutesIdle)
}

// EffectivePenalty combines scarcity with idle-time decay.
func EffectivePenalty(substitutesIdle int, idleMinutes float64) float64 {
	return ScarcityScore(substitutesIdle) * (1.0 - IdleBonus(idleMinutes))
}

// ApplyFillerPenalty scales filler-seat score worse for scarce, freshly-idle scopes.
func ApplyFillerPenalty(baseScore int, substitutesIdle int, idleMinutes float64) int {
	penalty := EffectivePenalty(substitutesIdle, idleMinutes)
	return int(float64(baseScore) * (1.0 + penalty*ScarcityPenaltyStrength))
}
