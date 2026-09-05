package shared

import (
	"time"

	"github.com/saucepan/hotpath/shared/cohort"
)

// SelectCohort picks an anchor (best task-fit) plus similar filler nodes (up to CohortMaxNodes).
func SelectCohort(nodes []NodeEvaluation, req TaskRequirements, now time.Time) []SelectorResult {
	eligible := SelectEligibleNodes(nodes, req, now, 0)
	if len(eligible) == 0 {
		return nil
	}

	anchorResult := eligible[0]
	anchorNode := nodeByID(nodes, anchorResult.NodeID)
	if anchorNode == nil {
		return []SelectorResult{anchorResult}
	}

	centroid := cohort.ComputeVector(specFromNode(*anchorNode))
	widths := cohort.BandWidthsFor(req.ScienceBand)

	result := []SelectorResult{anchorResult}
	remaining := eligible[1:]
	memberCount := 1

	for len(result) < CohortMaxNodes && len(remaining) > 0 {
		widths = cohort.AdaptiveBandWidth(widths, memberCount)
		bestIdx := -1
		var bestKey float64

		for i, cand := range remaining {
			candNode := nodeByID(nodes, cand.NodeID)
			if candNode == nil {
				continue
			}
			candVec := cohort.ComputeVector(specFromNode(*candNode))
			if !cohort.PassesBands(centroid, candVec, widths) {
				continue
			}
			substitutes := countSubstitutes(nodes, *candNode)
			idleMinutes := 0.0
			if candNode.IdleSinceMinutes != nil {
				idleMinutes = *candNode.IdleSinceMinutes
			}
			adjScore := ApplyFillerPenalty(cand.Score, substitutes, idleMinutes)
			cosine := cohort.WeightedCosine(centroid, candVec, cohort.DefaultWeights)
			key := float64(adjScore) - cosine*CohortCosineBonusWeight
			if bestIdx == -1 || key < bestKey {
				bestIdx, bestKey = i, key
			}
		}
		if bestIdx == -1 {
			break
		}

		picked := remaining[bestIdx]
		result = append(result, picked)
		pickedNode := nodeByID(nodes, picked.NodeID)
		if pickedNode == nil {
			break
		}
		pickedVec := cohort.ComputeVector(specFromNode(*pickedNode))
		memberCount++
		for i := range centroid {
			centroid[i] = (centroid[i]*float64(memberCount-1) + pickedVec[i]) / float64(memberCount)
		}
		remaining = append(remaining[:bestIdx], remaining[bestIdx+1:]...)
	}
	return result
}

func specFromNode(n NodeEvaluation) cohort.TelescopeSpec {
	spec := cohort.TelescopeSpec{Filters: n.AvailableFilters}
	if n.ApertureMM != nil {
		spec.ApertureMM = *n.ApertureMM
	}
	if n.FocalLengthMM != nil {
		spec.FocalLengthMM = *n.FocalLengthMM
	}
	if n.PixelSizeUM != nil {
		spec.PixelSizeUM = *n.PixelSizeUM
	}
	if n.FOVWidthArcmin != nil {
		spec.FOVWidthArcmin = *n.FOVWidthArcmin
	}
	if n.FOVHeightArcmin != nil {
		spec.FOVHeightArcmin = *n.FOVHeightArcmin
	}
	if n.SiteSeeingArcsec != nil {
		spec.SeeingArcsec = *n.SiteSeeingArcsec
	}
	if n.MountType != nil {
		spec.MountType = *n.MountType
	}
	return spec
}

func countSubstitutes(nodes []NodeEvaluation, n NodeEvaluation) int {
	vec := cohort.ComputeVector(specFromNode(n))
	count := 0
	for _, other := range nodes {
		if other.NodeID == n.NodeID || !nodeIsIdle(other) || other.CurrentTaskID != nil {
			continue
		}
		otherVec := cohort.ComputeVector(specFromNode(other))
		if cohort.PassesBands(vec, otherVec, cohort.DefaultBandWidths) {
			count++
		}
	}
	return count
}

func nodeByID(nodes []NodeEvaluation, nodeID string) *NodeEvaluation {
	for i := range nodes {
		if nodes[i].NodeID == nodeID {
			return &nodes[i]
		}
	}
	return nil
}
