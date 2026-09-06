package main

import (
	"testing"
	"time"
)

func TestCoverageLoopInterval(t *testing.T) {
	key := "SP_TEST_COVERAGE_LOOP_MIN"
	def := 30 * time.Minute

	if got := coverageLoopInterval(key, def); got != def {
		t.Fatalf("unset = %v, want default %v", got, def)
	}
	t.Setenv(key, "15")
	if got := coverageLoopInterval(key, def); got != 15*time.Minute {
		t.Fatalf("15 = %v, want 15m", got)
	}
	t.Setenv(key, "0")
	if got := coverageLoopInterval(key, def); got != def {
		t.Fatalf("0 should fall back to default, got %v", got)
	}
	t.Setenv(key, "-5")
	if got := coverageLoopInterval(key, def); got != def {
		t.Fatalf("negative should fall back to default, got %v", got)
	}
	t.Setenv(key, "not-a-number")
	if got := coverageLoopInterval(key, def); got != def {
		t.Fatalf("malformed should fall back to default, got %v", got)
	}
}

func TestStringOrEmptyJSON(t *testing.T) {
	if got := stringOrEmptyJSON(nil); got != "{}" {
		t.Fatalf("nil = %q, want {}", got)
	}
	if got := stringOrEmptyJSON([]byte{}); got != "{}" {
		t.Fatalf("empty slice = %q, want {}", got)
	}
	if got := stringOrEmptyJSON([]byte(`{"a":1}`)); got != `{"a":1}` {
		t.Fatalf("non-empty = %q, want passthrough", got)
	}
}
