package shared

// PlanPreemptCostMs converts remaining planned integration into a score penalty
// added when considering preemption (#421). Higher remaining → harder to preempt.
//
// Units: milliseconds of "virtual slew" so it composes with idleScore / slewMs.
// 1 remaining second → 1000 ms cost.
func PlanPreemptCostMs(remainingSec float64) int {
	if remainingSec <= 0 {
		return 0
	}
	return int(remainingSec * 1000.0)
}

// EffectivePreemptThreshold raises the priority-diff barrier when the victim
// still has planned work remaining. Each full minute of remaining plan adds +1
// to the required priority difference (on top of the base threshold).
func EffectivePreemptThreshold(baseThreshold int, remainingSec float64) int {
	if baseThreshold < 0 {
		baseThreshold = 0
	}
	if remainingSec <= 0 {
		return baseThreshold
	}
	extra := int(remainingSec / 60.0)
	return baseThreshold + extra
}

// MayPreempt reports whether priorityDiff (currentPriority - newPriority; lower
// new = more urgent) is enough given remaining plan cost (#421).
func MayPreempt(priorityDiff, baseThreshold int, remainingSec float64, nearby bool) bool {
	if priorityDiff <= 0 {
		return false
	}
	if nearby {
		need := 1
		if remainingSec > 0 {
			need = 1 + int(remainingSec/120.0)
		}
		return priorityDiff >= need
	}
	return priorityDiff >= EffectivePreemptThreshold(baseThreshold, remainingSec)
}
