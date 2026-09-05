package lanes

import (
	"testing"
	"time"

	"github.com/saucepan/hotpath/shared"
	"github.com/saucepan/hotpath/shared/campaign"
)

func TestClassifyLane(t *testing.T) {
	cases := []struct {
		name string
		in   SeasonInputs
		want Lane
	}{
		{"default planned", SeasonInputs{}, LaneInterrupt},
		{"continuous normal", SeasonInputs{Kind: "continuous", Urgency: "normal"}, LanePlanned},
		{"sparse", SeasonInputs{Kind: "sparse"}, LanePlanned},
		{"too", SeasonInputs{Kind: "too"}, LaneInterrupt},
		{"elevated", SeasonInputs{Urgency: "elevated"}, LaneInterrupt},
		{"critical", SeasonInputs{Urgency: "critical"}, LaneInterrupt},
		{"emergency", SeasonInputs{EmergencyHandoff: true}, LaneInterrupt},
		{"TOO case", SeasonInputs{Kind: "TOO"}, LaneInterrupt},
		{"continuous elevated", SeasonInputs{Kind: "continuous", Urgency: "elevated"}, LaneInterrupt},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyLane(tc.in); got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}

func TestFromPackSeason(t *testing.T) {
	ws, we := "2026-07-29T00:00:00Z", "2026-07-30T00:00:00Z"
	s := &campaign.SeasonIntent{
		Kind: "sparse", Urgency: "normal", CadenceGoalMin: 45,
		WindowStart: &ws, WindowEnd: &we,
	}
	in := FromPackSeason(s)
	if in.Kind != "sparse" || in.CadenceGoalMin != 45 || in.WindowStart == nil {
		t.Fatalf("unexpected %+v", in)
	}
	if FromPackSeason(nil).Kind != "" {
		t.Fatal("nil season should be empty")
	}
}

func TestBuildAgenda_cadenceAndWindow(t *testing.T) {
	now := time.Date(2026, 7, 29, 2, 0, 0, 0, time.UTC)
	ws := now
	we := now.Add(4 * time.Hour)
	ra, dec := 83.8, -5.4
	tasks := []PlanTask{
		{
			TaskID: 1, Priority: 50, RA: &ra, Dec: &dec,
			IntegrationSec: 600, CadenceGoalMin: 30,
			WindowStart: ws, WindowEnd: we,
		},
		{
			TaskID: 2, Priority: 40, RA: &ra, Dec: &dec,
			IntegrationSec: 300, CadenceGoalMin: 60,
			WindowStart: ws, WindowEnd: we,
		},
	}
	nodes := []PlanNode{{NodeID: "a"}, {NodeID: "b"}}
	slots := BuildAgenda(tasks, nodes, now)
	if len(slots) != 2 {
		t.Fatalf("want 2 slots, got %d: %+v", len(slots), slots)
	}
	byNode := GroupAgendaByNode(slots)
	if len(byNode) == 0 {
		t.Fatal("expected grouped agenda")
	}
	for _, s := range slots {
		if s.StartAt.Before(ws) || s.EndAt.After(we) {
			t.Fatalf("slot outside window: %+v", s)
		}
		if s.EndAt.Sub(s.StartAt) <= 0 {
			t.Fatalf("bad duration: %+v", s)
		}
	}
}

func TestBuildAgenda_pastWindowSkipped(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	tasks := []PlanTask{{
		TaskID: 1, IntegrationSec: 60,
		WindowStart: now.Add(-3 * time.Hour),
		WindowEnd:   now.Add(-1 * time.Hour),
	}}
	slots := BuildAgenda(tasks, []PlanNode{{NodeID: "a"}}, now)
	if len(slots) != 0 {
		t.Fatalf("past window should skip, got %+v", slots)
	}
}

func TestAgendaRemaining(t *testing.T) {
	now := time.Date(2026, 7, 29, 2, 30, 0, 0, time.UTC)
	s := AgendaSlot{
		StartAt: now.Add(-10 * time.Minute),
		EndAt:   now.Add(35 * time.Minute),
	}
	rem := s.RemainingSec(now)
	if rem < 34*60 || rem > 36*60 {
		t.Fatalf("remaining=%v want ~35min", rem)
	}
	got := ActiveRemainingSec([]AgendaSlot{s}, now)
	if got != rem {
		t.Fatalf("active remaining=%v want %v", got, rem)
	}
}

func TestMayPreempt_planAware(t *testing.T) {
	// Mid 45-min integration with 5 min left vs 40 min left.
	if !shared.MayPreempt(15, 10, 5*60, false) {
		t.Fatal("15 diff should clear base 10 + 5min extra(=5) → need 15")
	}
	if shared.MayPreempt(14, 10, 5*60, false) {
		t.Fatal("14 should not clear need 15")
	}
	if shared.MayPreempt(10, 10, 40*60, false) {
		t.Fatal("40 min remaining should raise threshold above 10")
	}
	if !shared.MayPreempt(1, 10, 0, true) {
		t.Fatal("nearby with no plan should allow any positive diff")
	}
	if shared.MayPreempt(1, 10, 240*60, true) {
		t.Fatal("nearby with huge remaining should still raise barrier")
	}
}

func TestPlanPreemptCostMs(t *testing.T) {
	if shared.PlanPreemptCostMs(0) != 0 {
		t.Fatal("zero remaining")
	}
	if shared.PlanPreemptCostMs(40*60) != 40*60*1000 {
		t.Fatalf("cost=%d", shared.PlanPreemptCostMs(40*60))
	}
}

func TestPreferRoster_failOpen(t *testing.T) {
	nodes := []shared.NodeEvaluation{{NodeID: "a"}, {NodeID: "b"}}
	got := PreferRoster(nodes, nil)
	if len(got) != 2 {
		t.Fatal("empty roster fail-open")
	}
	got = PreferRoster(nodes, []StandbyEntry{{NodeID: "missing"}})
	if len(got) != 2 {
		t.Fatal("no hits should fail-open to all")
	}
	got = PreferRoster(nodes, []StandbyEntry{{NodeID: "b"}, {NodeID: "a"}})
	if len(got) != 2 || got[0].NodeID != "b" {
		t.Fatalf("want roster order b,a got %+v", got)
	}
}

func TestBuildStandbyFromEligible(t *testing.T) {
	el := []shared.SelectorResult{
		{NodeID: "x", Score: 10},
		{NodeID: "y", Score: 5},
	}
	r := BuildStandbyFromEligible(el, 1)
	if len(r) != 1 || r[0].NodeID != "x" {
		t.Fatalf("limit 1: %+v", r)
	}
}

func TestRankStandby(t *testing.T) {
	r := RankStandby([]string{"b", "a", "c"}, func(id string) (int, bool) {
		switch id {
		case "a":
			return 20, true
		case "b":
			return 10, true
		default:
			return 0, false
		}
	}, 0)
	if len(r) != 2 || r[0].NodeID != "b" || r[1].NodeID != "a" {
		t.Fatalf("got %+v", r)
	}
}

func TestParseWindow(t *testing.T) {
	now := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	ws, we := ParseWindow(nil, nil, now)
	if !ws.Equal(now) || we.Sub(ws) != PlanHorizonDefault {
		t.Fatalf("defaults ws=%v we=%v", ws, we)
	}
	s, e := "2026-07-29T01:00:00Z", "2026-07-29T05:00:00Z"
	ws, we = ParseWindow(&s, &e, now)
	if ws.Hour() != 1 || we.Hour() != 5 {
		t.Fatalf("parsed ws=%v we=%v", ws, we)
	}
}

func TestAlertClass(t *testing.T) {
	if AlertClass(SeasonInputs{Kind: "too"}) != "too" {
		t.Fatal("too")
	}
	if AlertClass(SeasonInputs{Urgency: "critical"}) != "critical" {
		t.Fatal("critical")
	}
	if AlertClass(SeasonInputs{}) != "default" {
		t.Fatal("default")
	}
}
