package lanes

import (
	"sort"
	"time"
)

// PlanTask is one planned-lane input for the periodic planner stub (#421).
type PlanTask struct {
	TaskID         int
	CampaignID     string
	Priority       int
	RA             *float64
	Dec            *float64
	IntegrationSec float64
	CadenceGoalMin int
	WindowStart    time.Time
	WindowEnd      time.Time
	PreferredNodes []string // optional coverage preferred; empty = any
}

// PlanNode is a lightweight fleet row for planning (id + optional preference).
type PlanNode struct {
	NodeID string
}

// PlanHorizonDefault is how far ahead the stub schedules when windows are open-ended.
const PlanHorizonDefault = 6 * time.Hour

// BuildAgenda schedules planned tasks onto nodes using pack window_* and
// cadence_goal_min. This is a deterministic stub: round-robin nodes, place the
// next visit at max(window_start, last_end + cadence) within [window_start, window_end].
//
// Production reclaim / lease expiry is #403 — this only writes an in-memory agenda.
func BuildAgenda(tasks []PlanTask, nodes []PlanNode, now time.Time) []AgendaSlot {
	if len(tasks) == 0 || len(nodes) == 0 {
		return nil
	}
	// Stable order for hermetic tests.
	sorted := append([]PlanTask(nil), tasks...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Priority != sorted[j].Priority {
			return sorted[i].Priority < sorted[j].Priority // lower = more urgent
		}
		return sorted[i].TaskID < sorted[j].TaskID
	})

	nextFree := make(map[string]time.Time, len(nodes))
	for _, n := range nodes {
		nextFree[n.NodeID] = now
	}

	var out []AgendaSlot
	nodeIdx := 0
	for _, t := range sorted {
		winStart := t.WindowStart
		winEnd := t.WindowEnd
		if winStart.IsZero() {
			winStart = now
		}
		if winEnd.IsZero() || !winEnd.After(winStart) {
			winEnd = winStart.Add(PlanHorizonDefault)
		}
		if winEnd.Before(now) {
			continue // past window — skip
		}

		integ := t.IntegrationSec
		if integ <= 0 {
			integ = 60
		}
		cadence := time.Duration(t.CadenceGoalMin) * time.Minute
		if t.CadenceGoalMin <= 0 {
			cadence = time.Duration(integ) * time.Second
			if cadence < time.Minute {
				cadence = time.Minute
			}
		}

		candidates := nodes
		if len(t.PreferredNodes) > 0 {
			set := make(map[string]struct{}, len(t.PreferredNodes))
			for _, id := range t.PreferredNodes {
				set[id] = struct{}{}
			}
			var pref []PlanNode
			for _, n := range nodes {
				if _, ok := set[n.NodeID]; ok {
					pref = append(pref, n)
				}
			}
			if len(pref) > 0 {
				candidates = pref
			}
		}

		// Pick the candidate free earliest (round-robin tie-break).
		bestID := ""
		bestStart := time.Time{}
		for i := 0; i < len(candidates); i++ {
			n := candidates[(nodeIdx+i)%len(candidates)]
			start := nextFree[n.NodeID]
			if start.Before(winStart) {
				start = winStart
			}
			if start.Before(now) {
				start = now
			}
			end := start.Add(time.Duration(integ * float64(time.Second)))
			if end.After(winEnd) {
				continue
			}
			if bestID == "" || start.Before(bestStart) {
				bestID = n.NodeID
				bestStart = start
			}
		}
		if bestID == "" {
			continue
		}
		end := bestStart.Add(time.Duration(integ * float64(time.Second)))
		out = append(out, AgendaSlot{
			TaskID:         t.TaskID,
			CampaignID:     t.CampaignID,
			NodeID:         bestID,
			StartAt:        bestStart,
			EndAt:          end,
			CadenceGoalMin: t.CadenceGoalMin,
			Priority:       t.Priority,
			RA:             t.RA,
			Dec:            t.Dec,
		})
		// Next visit for this node respects cadence after this slot.
		next := end.Add(cadence)
		if next.Before(end) {
			next = end
		}
		nextFree[bestID] = next
		nodeIdx++
	}
	return out
}

// GroupAgendaByNode buckets slots by node_id.
func GroupAgendaByNode(slots []AgendaSlot) map[string][]AgendaSlot {
	out := make(map[string][]AgendaSlot)
	for _, s := range slots {
		out[s.NodeID] = append(out[s.NodeID], s)
	}
	for id := range out {
		sort.Slice(out[id], func(i, j int) bool {
			return out[id][i].StartAt.Before(out[id][j].StartAt)
		})
	}
	return out
}

// ParseWindow parses optional RFC3339 window bounds; zero time if unset/invalid.
func ParseWindow(start, end *string, now time.Time) (time.Time, time.Time) {
	var ws, we time.Time
	if start != nil && *start != "" {
		if t, err := time.Parse(time.RFC3339, *start); err == nil {
			ws = t.UTC()
		} else if t, err := time.Parse(time.RFC3339Nano, *start); err == nil {
			ws = t.UTC()
		}
	}
	if end != nil && *end != "" {
		if t, err := time.Parse(time.RFC3339, *end); err == nil {
			we = t.UTC()
		} else if t, err := time.Parse(time.RFC3339Nano, *end); err == nil {
			we = t.UTC()
		}
	}
	if ws.IsZero() {
		ws = now
	}
	if we.IsZero() {
		we = ws.Add(PlanHorizonDefault)
	}
	return ws, we
}
