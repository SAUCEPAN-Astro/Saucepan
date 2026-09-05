package simultaneous

import "testing"

func TestIsScientificSuccess(t *testing.T) {
	tests := []struct {
		name   string
		status string
		want   bool
	}{
		{"pending", StatusPending, false},
		{"locking", StatusLocking, false},
		{"locked", StatusLocked, false},
		{"partial", StatusPartial, false},
		{"failed", StatusFailed, false},
		{"completed", StatusCompleted, true},
		{"empty string", "", false},
		{"unknown status", "bogus", false},
		{"case sensitive mismatch", "Completed", false},
		{"whitespace padded", " completed", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsScientificSuccess(tt.status); got != tt.want {
				t.Fatalf("IsScientificSuccess(%q) = %v, want %v", tt.status, got, tt.want)
			}
		})
	}
}

func TestStatusConstantsAreDistinct(t *testing.T) {
	all := []string{StatusPending, StatusLocking, StatusLocked, StatusPartial, StatusFailed, StatusCompleted}
	seen := map[string]struct{}{}
	for _, s := range all {
		if _, dup := seen[s]; dup {
			t.Fatalf("duplicate status constant value %q", s)
		}
		seen[s] = struct{}{}
	}
	if len(seen) != 6 {
		t.Fatalf("expected 6 distinct status constants, got %d", len(seen))
	}
}
