package shared

import "testing"

func TestFilterPreferredNodes(t *testing.T) {
	nodes := []NodeEvaluation{
		{NodeID: "a"},
		{NodeID: "b"},
		{NodeID: "c"},
	}
	got := FilterPreferredNodes(nodes, []string{"b", "c"})
	if len(got) != 2 || got[0].NodeID != "b" || got[1].NodeID != "c" {
		t.Fatalf("got %+v", got)
	}
	// fail-open when none preferred online (soft default)
	got = FilterPreferredNodes(nodes, []string{"z"})
	if len(got) != 3 {
		t.Fatalf("fail-open want 3, got %d", len(got))
	}
	// hard mode: empty when none preferred online
	got = FilterPreferredNodesMode(nodes, []string{"z"}, false)
	if got != nil && len(got) != 0 {
		t.Fatalf("hard mode want empty, got %+v", got)
	}
}
