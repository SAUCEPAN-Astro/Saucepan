package campaign

import "testing"

func TestAllowsAssign(t *testing.T) {
	if !AllowsAssign("") {
		t.Fatal("standalone tasks should assign")
	}
	if !AllowsAssign(StatusActive) {
		t.Fatal("active campaigns should assign")
	}
	for _, s := range []string{StatusDraft, StatusPaused, StatusCompleted, StatusArchived} {
		if AllowsAssign(s) {
			t.Fatalf("status %q should block assign", s)
		}
	}
}

func TestCanComplete(t *testing.T) {
	if !CanComplete(StatusActive) {
		t.Fatal("active should allow complete")
	}
	if CanComplete(StatusPaused) || CanComplete(StatusDraft) {
		t.Fatal("only active should complete")
	}
}
