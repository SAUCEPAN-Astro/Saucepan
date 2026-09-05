package shared

import (
	"testing"
	"time"
)

func TestUrgencyForTaskEmergency(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	emerg := now.Add(-time.Minute)
	task := HandoffTask{
		ID:                          1,
		Status:                      "assigned",
		EmergencyHandoffRequestedAt: &emerg,
	}
	if UrgencyForTask(task, now) != UrgencyEmergency {
		t.Fatal("expected emergency urgency")
	}
}

func TestRecommendedPollSeconds(t *testing.T) {
	if RecommendedPollSeconds(UrgencyObstruction) != PollObstructionSeconds {
		t.Fatal("obstruction poll mismatch")
	}
}

func TestEffectiveAssignPriorityBoost(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	end := now.Add(30 * time.Minute)
	lead := 5400
	task := HandoffTask{
		ID:                 1,
		Status:             "pending",
		ScheduledEndAt:     &end,
		HandoffLeadSeconds: &lead,
	}
	// Within lead window → planned urgency → base 30 becomes 10
	got := EffectiveAssignPriority(30, task, now)
	if got != 10 {
		t.Fatalf("planned boost: got %d want 10", got)
	}
	// No handoff fields → unchanged
	plain := HandoffTask{ID: 2, Status: "pending"}
	if EffectiveAssignPriority(30, plain, now) != 30 {
		t.Fatal("expected no boost without handoff window")
	}
	emerg := now
	em := HandoffTask{ID: 3, Status: "pending", EmergencyHandoffRequestedAt: &emerg}
	if EffectiveAssignPriority(50, em, now) != 1 {
		t.Fatal("emergency should floor at priority 1")
	}
}

func TestValidateObstructionMask(t *testing.T) {
	bad := ObstructionMask{{{30, 0}, {30}}}
	if ValidateObstructionMask(bad) == nil {
		t.Fatal("expected invalid mask")
	}
	good := ObstructionMask{{{30, 0}, {30, 90}, {10, 90}, {10, 0}}}
	if ValidateObstructionMask(good) != nil {
		t.Fatal("expected valid mask")
	}
}
