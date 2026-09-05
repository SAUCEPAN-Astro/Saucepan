package lanes

import (
	"sort"

	"github.com/saucepan/hotpath/shared"
)

// StandbyEntry is one ranked node in an interrupt-class roster.
type StandbyEntry struct {
	NodeID string
	Score  int // lower = better (mirrors idleScore convention)
}

// DefaultStandbyLimit caps roster size for interrupt cache hits.
const DefaultStandbyLimit = 16

// RankStandby builds a ranked standby list from node IDs + scoreFn.
// scoreFn returns (score, ok); ok=false skips the node.
func RankStandby(nodeIDs []string, scoreFn func(nodeID string) (score int, ok bool), limit int) []StandbyEntry {
	var out []StandbyEntry
	for _, id := range nodeIDs {
		sc, ok := scoreFn(id)
		if !ok {
			continue
		}
		out = append(out, StandbyEntry{NodeID: id, Score: sc})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score < out[j].Score
		}
		return out[i].NodeID < out[j].NodeID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// BuildStandbyFromEligible converts SelectEligibleNodes results into a roster.
func BuildStandbyFromEligible(eligible []shared.SelectorResult, limit int) []StandbyEntry {
	out := make([]StandbyEntry, 0, len(eligible))
	for _, e := range eligible {
		out = append(out, StandbyEntry{NodeID: e.NodeID, Score: e.Score})
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// PreferRoster restricts the fleet to roster hits when any exist; otherwise
// returns all nodes (fail-open so a stale/empty roster never blocks ToO).
func PreferRoster(nodes []shared.NodeEvaluation, roster []StandbyEntry) []shared.NodeEvaluation {
	if len(roster) == 0 || len(nodes) == 0 {
		return nodes
	}
	set := make(map[string]struct{}, len(roster))
	order := make([]string, 0, len(roster))
	for _, e := range roster {
		if e.NodeID == "" {
			continue
		}
		if _, ok := set[e.NodeID]; ok {
			continue
		}
		set[e.NodeID] = struct{}{}
		order = append(order, e.NodeID)
	}
	if len(set) == 0 {
		return nodes
	}
	byID := make(map[string]shared.NodeEvaluation, len(nodes))
	for _, n := range nodes {
		byID[n.NodeID] = n
	}
	var out []shared.NodeEvaluation
	for _, id := range order {
		if n, ok := byID[id]; ok {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return nodes // fail-open
	}
	return out
}
