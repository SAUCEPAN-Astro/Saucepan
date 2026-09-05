// Package lanes splits task assignment into planned vs interrupt paths (#421).
//
// Planned work uses a periodic planner and per-node Redis agendas.
// Interrupt (ToO / elevated) keeps the hot-path NOTIFY → select → MQTT flow,
// optionally consulting a standby roster and plan-aware preemption cost.
package lanes

import (
	"strings"

	"github.com/saucepan/hotpath/shared/campaign"
)

// Lane is the assignment path for a task.
type Lane string

const (
	LanePlanned   Lane = "planned"
	LaneInterrupt Lane = "interrupt"
)

// SeasonInputs are the pack season fields the planned lane consumes (#421).
// Interim: brains may leave these unset; ClassifyLane then defaults to planned
// unless urgency/kind/emergency signals an interrupt. Orchestrator fills these
// from campaigns.pack_json at assign load (brain→task materialization interim).
type SeasonInputs struct {
	Kind           string // continuous | sparse | too
	Urgency        string // normal | elevated | critical
	CadenceGoalMin int
	WindowStart    *string
	WindowEnd      *string
	// EmergencyHandoff forces interrupt regardless of season.
	EmergencyHandoff bool
}

// FromPackSeason maps campaign.SeasonIntent into SeasonInputs.
func FromPackSeason(s *campaign.SeasonIntent) SeasonInputs {
	if s == nil {
		return SeasonInputs{}
	}
	return SeasonInputs{
		Kind:           strings.TrimSpace(s.Kind),
		Urgency:        strings.TrimSpace(s.Urgency),
		CadenceGoalMin: s.CadenceGoalMin,
		WindowStart:    s.WindowStart,
		WindowEnd:      s.WindowEnd,
	}
}

// ClassifyLane chooses planned vs interrupt.
//
// Interrupt when:
//   - season.kind == "too"
//   - season.urgency is elevated or critical
//   - emergency handoff is set
//   - season is unset (legacy / brain→task interim: keep hot path until packs carry season)
//
// Planned when season.kind is continuous or sparse (pack windows + cadence apply).
func ClassifyLane(in SeasonInputs) Lane {
	if in.EmergencyHandoff {
		return LaneInterrupt
	}
	kind := strings.ToLower(strings.TrimSpace(in.Kind))
	if kind == "too" {
		return LaneInterrupt
	}
	urg := strings.ToLower(strings.TrimSpace(in.Urgency))
	if urg == "elevated" || urg == "critical" {
		return LaneInterrupt
	}
	if kind == "continuous" || kind == "sparse" {
		return LanePlanned
	}
	// No season on the task yet — interim: interrupt hot path (do not stall ad-hoc tasks).
	return LaneInterrupt
}

// AlertClass is the standby-roster bucket for an interrupt task.
func AlertClass(in SeasonInputs) string {
	kind := strings.ToLower(strings.TrimSpace(in.Kind))
	if kind == "too" {
		return "too"
	}
	urg := strings.ToLower(strings.TrimSpace(in.Urgency))
	switch urg {
	case "critical":
		return "critical"
	case "elevated":
		return "elevated"
	default:
		return "default"
	}
}
