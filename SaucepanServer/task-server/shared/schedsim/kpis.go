// Package schedsim is an offline / hermetic scheduler simulator (#420).
// It scores SelectCohort / SelectBestNode assignment changes without Redis, PG, MQTT, or a VPS.
// Distinct from #407 (unit/integration path tests): this package owns fleet KPI scoring + replay.
package schedsim

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Report is the KPI scorecard for one simulation run.
type Report struct {
	FramesCaptured      int                `json:"frames_captured"`
	ScienceValue        float64            `json:"science_value"`
	DeadlineMisses      int                `json:"deadline_misses"`
	NodeUtilization     float64            `json:"node_utilization"`
	FairnessCoefficient float64            `json:"fairness_coefficient"`
	DuplicateWaves      int                `json:"duplicate_assign_waves"`
	TasksAssigned       int                `json:"tasks_assigned"`
	TasksUnassigned     int                `json:"tasks_unassigned"`
	Assignments         []AssignmentEvent  `json:"assignments,omitempty"`
	FramesPerNode       map[string]int     `json:"frames_per_node,omitempty"`
	BusySecondsPerNode  map[string]float64 `json:"busy_seconds_per_node,omitempty"`
}

// ScienceQualityProxy is the per-seat quality multiplier used in the science-value proxy.
// quality = reliability × min(1, aperture_mm / 200). Documented proxy — not real photometry.
func ScienceQualityProxy(reliability, apertureMM float64) float64 {
	if reliability <= 0 {
		reliability = 1.0
	}
	apNorm := 1.0
	if apertureMM > 0 {
		apNorm = apertureMM / 200.0
		if apNorm > 1.0 {
			apNorm = 1.0
		}
	}
	return reliability * apNorm
}

// JainFairness returns Jain's fairness index over non-negative x_i.
// 1 = equal share; approaches 1/N when one node takes everything. Empty → 1.
func JainFairness(values []float64) float64 {
	if len(values) == 0 {
		return 1.0
	}
	var sum, sumSq float64
	for _, v := range values {
		if v < 0 {
			v = 0
		}
		sum += v
		sumSq += v * v
	}
	if sumSq == 0 {
		return 1.0
	}
	n := float64(len(values))
	return (sum * sum) / (n * sumSq)
}

// FormatReport returns a human-readable scorecard for CLI / PR paste.
func FormatReport(r Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "frames_captured=%d\n", r.FramesCaptured)
	fmt.Fprintf(&b, "science_value=%.4f\n", r.ScienceValue)
	fmt.Fprintf(&b, "deadline_misses=%d\n", r.DeadlineMisses)
	fmt.Fprintf(&b, "node_utilization=%.4f\n", r.NodeUtilization)
	fmt.Fprintf(&b, "fairness_coefficient=%.4f\n", r.FairnessCoefficient)
	fmt.Fprintf(&b, "duplicate_assign_waves=%d\n", r.DuplicateWaves)
	fmt.Fprintf(&b, "tasks_assigned=%d tasks_unassigned=%d\n", r.TasksAssigned, r.TasksUnassigned)
	if len(r.FramesPerNode) > 0 {
		ids := make([]string, 0, len(r.FramesPerNode))
		for id := range r.FramesPerNode {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		fmt.Fprintf(&b, "frames_per_node:")
		for _, id := range ids {
			fmt.Fprintf(&b, " %s=%d", id, r.FramesPerNode[id])
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// round4 rounds to 4 decimal places for stable test asserts.
func round4(v float64) float64 {
	return math.Round(v*10000) / 10000
}
