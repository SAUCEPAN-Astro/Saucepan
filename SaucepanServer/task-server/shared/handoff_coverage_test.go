package shared

import (
	"testing"
	"time"
)

func TestCoverageActiveUrgency(t *testing.T) {
	now := time.Now().UTC()
	task := HandoffTask{CoverageActive: true}
	if UrgencyForTask(task, now) != UrgencyPlanned {
		t.Fatal("coverage active should be planned urgency")
	}
	pri := EffectiveAssignPriority(50, task, now)
	if pri >= 50 {
		t.Fatalf("expected boost, got %d", pri)
	}
}
