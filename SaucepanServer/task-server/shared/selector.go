package shared

import (
	"sort"
	"time"
)

// NodeEvaluation — per-node data the orchestrator gathers for selection.
type NodeEvaluation struct {
	NodeID              string
	Status              string
	Power               float64
	LimitingMagnitude   *float64
	CurrentTaskID       *int
	CurrentTaskPriority *int
	EstimatedStartupMS  int
	QualityTier         string
	ReliabilityScore    float64
	MountAltDeg         *float64
	MountAzDeg          *float64
	SiteLat             *float64
	SiteLon             *float64
	MountSlewRateDegS   *float64
	AvailableFilters    []string
	IsEmulator          bool
	ObstructionMask     ObstructionMask
	MountLimits         *MountLimits
	HorizonProfile      *HorizonProfile
	ApertureMM          *float64
	FocalLengthMM       *float64
	PixelSizeUM         *float64
	FOVWidthArcmin      *float64
	FOVHeightArcmin     *float64
	MountType           *int
	MaxStableExposureS  *float64
	SiteSeeingArcsec    *float64
	IdleSinceMinutes    *float64
	EnabledCampaignIDs  []string
	// PlanRemainingSec is remaining planned integration / agenda value (#421).
	// Nil or 0 → preemption falls back to priority integers only.
	PlanRemainingSec *float64
}

// SelectorResult — one eligible node and why it was picked.
type SelectorResult struct {
	NodeID     string
	SlewTimeMs int
	IsIdle     bool
	IsNearby   bool
	Preempting bool
	PrevTaskID *int
	Score      int
	Reason     string
}

type nodeSlewInfo struct {
	slewMs   int
	canSlew  bool
	isNearby bool
}

func nodeTelescopeSafety(n NodeEvaluation) TelescopeSafety {
	s := TelescopeSafety{
		ObstructionMask: n.ObstructionMask,
		MountLimits:     n.MountLimits,
		HorizonProfile:  n.HorizonProfile,
		MountAltDeg:     n.MountAltDeg,
		MountAzDeg:      n.MountAzDeg,
		SiteLat:         n.SiteLat,
		SiteLon:         n.SiteLon,
	}
	return s
}

func computeNodeSlew(n NodeEvaluation, req TaskRequirements, slewNearbyMs int, now time.Time) nodeSlewInfo {
	haveTarget := req.RA != nil && req.Dec != nil && n.SiteLat != nil && n.SiteLon != nil
	if !haveTarget {
		return nodeSlewInfo{}
	}
	targetAlt, targetAz := ComputeTargetAltAz(*req.RA, *req.Dec, *n.SiteLat, *n.SiteLon, now)
	if n.MountAltDeg == nil || n.MountAzDeg == nil || n.MountSlewRateDegS == nil || *n.MountSlewRateDegS <= 0 {
		return nodeSlewInfo{}
	}
	slewMs := EstimateSlewTimeMs(*n.MountAltDeg, *n.MountAzDeg, targetAlt, targetAz, n.MountSlewRateDegS)
	isNearby := slewNearbyMs > 0 && slewMs > 0 && slewMs < slewNearbyMs
	return nodeSlewInfo{slewMs: slewMs, canSlew: true, isNearby: isNearby}
}

func idleScore(n NodeEvaluation, slewMs int) int {
	score := n.EstimatedStartupMS + slewMs
	switch n.QualityTier {
	case "premium":
		score = int(float64(score) * 0.7)
	case "community":
		score = int(float64(score) * 1.3)
	}
	score = int(float64(score) * (2.0 - n.ReliabilityScore))
	return score
}

func nodeIsIdle(n NodeEvaluation) bool {
	return n.Status == "" || n.Status == NodeStatusIdle || n.Status == NodeStatusOnline
}

func idleSelectorResult(n NodeEvaluation, slew nodeSlewInfo, reason string) SelectorResult {
	return SelectorResult{
		NodeID:     n.NodeID,
		SlewTimeMs: slew.slewMs,
		IsIdle:     true,
		IsNearby:   slew.isNearby,
		Score:      idleScore(n, slew.slewMs),
		Reason:     reason,
	}
}

func preemptionSelectorResult(n NodeEvaluation, slew nodeSlewInfo, taskPriority, preemptThreshold int) *SelectorResult {
	if n.CurrentTaskPriority == nil {
		return &SelectorResult{
			NodeID:     n.NodeID,
			SlewTimeMs: slew.slewMs,
			IsIdle:     true,
			IsNearby:   slew.isNearby,
			Score:      idleScore(n, slew.slewMs),
			Reason:     "idle-ish (no current task)",
		}
	}

	priorityDiff := *n.CurrentTaskPriority - taskPriority
	planRem := 0.0
	if n.PlanRemainingSec != nil {
		planRem = *n.PlanRemainingSec
	}
	planCost := PlanPreemptCostMs(planRem)
	if slew.isNearby {
		if !MayPreempt(priorityDiff, preemptThreshold, planRem, true) {
			return nil
		}
		return &SelectorResult{
			NodeID:     n.NodeID,
			SlewTimeMs: slew.slewMs,
			IsNearby:   true,
			Preempting: true,
			PrevTaskID: n.CurrentTaskID,
			Score:      slew.slewMs + planCost,
			Reason:     "nearby_preempt",
		}
	}

	if !MayPreempt(priorityDiff, preemptThreshold, planRem, false) {
		return nil
	}
	score := slew.slewMs + n.EstimatedStartupMS + planCost
	switch n.QualityTier {
	case "premium":
		score = int(float64(score) * 0.7)
	case "community":
		score = int(float64(score) * 1.3)
	}
	return &SelectorResult{
		NodeID:     n.NodeID,
		SlewTimeMs: slew.slewMs,
		Preempting: true,
		PrevTaskID: n.CurrentTaskID,
		Score:      score,
		Reason:     "priority_preempt",
	}
}

func passesNodeGates(n NodeEvaluation, req TaskRequirements, now time.Time) bool {
	if n.IsEmulator != req.AllowEmulator {
		return false
	}
	if n.Status == NodeStatusOffline || n.Status == "error" {
		return false
	}
	if !nodeHasFilters(n.AvailableFilters, req.RequiredFilters) {
		return false
	}
	// Power is a declared telescope capability. Unlike optional optics fields,
	// a request that names a minimum power must fail closed when the node has no
	// usable value (zero is the registration default).
	if req.MinPower != nil && n.Power < *req.MinPower {
		return false
	}
	if req.TargetMagnitude != nil &&
		(n.LimitingMagnitude == nil || *n.LimitingMagnitude < *req.TargetMagnitude) {
		return false
	}
	// Target presence and site-coord presence are different things: a request
	// with no RA/Dec doesn't need an AltAz check, but a request that DOES have
	// a target must reject nodes with unknown site coords rather than skip the
	// check (#453) — missing location means "cannot evaluate", not "safe".
	hasTarget := req.RA != nil && req.Dec != nil
	if hasTarget {
		if n.SiteLat == nil || n.SiteLon == nil {
			return false
		}
		if !PassesAltAzSafety(*req.RA, *req.Dec, req.MinAltitudeDeg, nodeTelescopeSafety(n), now) {
			return false
		}
	}
	if req.MinApertureMM != nil && n.ApertureMM != nil && *n.ApertureMM < *req.MinApertureMM {
		return false
	}
	if req.MinSubExposureS != nil && n.MaxStableExposureS != nil && *n.MaxStableExposureS < *req.MinSubExposureS {
		return false
	}
	if n.FocalLengthMM != nil && n.PixelSizeUM != nil {
		plateScale := PlateScaleArcsecPerPx(*n.FocalLengthMM, *n.PixelSizeUM)
		if req.MinResolutionArcsec != nil && plateScale < *req.MinResolutionArcsec {
			return false
		}
		if req.MaxResolutionArcsec != nil && plateScale > *req.MaxResolutionArcsec {
			return false
		}
	}
	if n.ApertureMM != nil && n.SiteSeeingArcsec != nil {
		predicted := PredictedPSFArcsec(*n.ApertureMM, *n.SiteSeeingArcsec)
		if req.MinPSFFWHMArcsec != nil && predicted < *req.MinPSFFWHMArcsec {
			return false
		}
		if req.MaxPSFFWHMArcsec != nil && predicted > *req.MaxPSFFWHMArcsec {
			return false
		}
	}
	if req.RequiredFOVWidthArcmin != nil && n.FOVWidthArcmin != nil &&
		*n.FOVWidthArcmin < *req.RequiredFOVWidthArcmin*FOVHeadroomFactor {
		return false
	}
	if req.RequiredFOVHeightArcmin != nil && n.FOVHeightArcmin != nil &&
		*n.FOVHeightArcmin < *req.RequiredFOVHeightArcmin*FOVHeadroomFactor {
		return false
	}
	return true
}

// SelectEligibleNodes returns idle nodes that pass gates, sorted by task-fit score.
func SelectEligibleNodes(
	nodes []NodeEvaluation,
	req TaskRequirements,
	now time.Time,
	limit int,
) []SelectorResult {
	var out []SelectorResult
	for _, n := range nodes {
		if !nodeIsIdle(n) || n.CurrentTaskID != nil {
			continue
		}
		if !passesNodeGates(n, req, now) {
			continue
		}
		slew := computeNodeSlew(n, req, 0, now)
		out = append(out, idleSelectorResult(n, slew, "idle"))
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Score < out[j].Score
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// SelectBestNode evaluates all active nodes; retains preemption logic for single-assign fallback.
func SelectBestNode(
	nodes []NodeEvaluation,
	req TaskRequirements,
	taskPriority int,
	preemptThreshold, slewNearbyMs int,
	now time.Time,
) *SelectorResult {
	var best *SelectorResult

	for _, n := range nodes {
		if !passesNodeGates(n, req, now) {
			continue
		}
		slew := computeNodeSlew(n, req, slewNearbyMs, now)
		isIdle := nodeIsIdle(n)

		if isIdle && n.CurrentTaskID == nil {
			candidate := idleSelectorResult(n, slew, "idle")
			if best == nil || candidate.Score < best.Score {
				best = &candidate
			}
			continue
		}

		if n.CurrentTaskPriority == nil || n.Status == NodeStatusBusy || n.Status == "observing" ||
			n.Status == "uploading" {
			candidate := preemptionSelectorResult(n, slew, taskPriority, preemptThreshold)
			if candidate != nil && (best == nil || candidate.Score < best.Score) {
				best = candidate
			}
		}
	}

	return best
}

func nodeHasFilters(availableFilters, requiredFilters []string) bool {
	if len(requiredFilters) == 0 {
		return true
	}
	if len(availableFilters) == 0 {
		return false
	}
	filterSet := make(map[string]struct{}, len(availableFilters))
	for _, f := range availableFilters {
		filterSet[f] = struct{}{}
	}
	for _, req := range requiredFilters {
		if _, ok := filterSet[req]; !ok {
			return false
		}
	}
	return true
}

// FilterPreferredNodes restricts candidates to coverage-preferred telescope IDs.
// When failOpen is true (soft mode): if none of the preferred nodes are in the
// pool, returns all nodes. When failOpen is false (hard mode): returns empty.
func FilterPreferredNodes(nodes []NodeEvaluation, preferred []string) []NodeEvaluation {
	return FilterPreferredNodesMode(nodes, preferred, true)
}

// FilterPreferredNodesMode is FilterPreferredNodes with explicit fail-open control (#397).
func FilterPreferredNodesMode(nodes []NodeEvaluation, preferred []string, failOpen bool) []NodeEvaluation {
	if len(preferred) == 0 || len(nodes) == 0 {
		return nodes
	}
	set := make(map[string]struct{}, len(preferred))
	for _, id := range preferred {
		if id != "" {
			set[id] = struct{}{}
		}
	}
	if len(set) == 0 {
		return nodes
	}
	var out []NodeEvaluation
	for _, n := range nodes {
		if _, ok := set[n.NodeID]; ok {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		if failOpen {
			return nodes
		}
		return nil
	}
	return out
}
