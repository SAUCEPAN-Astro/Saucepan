package campaign

import "testing"

func TestNodeServesCampaign(t *testing.T) {
	enabled := []string{"c1", "c2"}
	if !NodeServesCampaign(enabled, "") {
		t.Fatal("standalone tasks should assign")
	}
	if !NodeServesCampaign(enabled, "c1") {
		t.Fatal("enabled campaign should assign")
	}
	if NodeServesCampaign(enabled, "c3") {
		t.Fatal("non-enabled campaign should block")
	}
	if NodeServesCampaign(nil, "c1") {
		t.Fatal("empty enabled should block campaign tasks")
	}
}
