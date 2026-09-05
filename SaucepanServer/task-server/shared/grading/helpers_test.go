package grading

import (
	"math"
	"testing"
	"time"
)

// helpers_test.go exercises the small pure helpers behind dimensions.go and
// points.go (clamp, rounding, type coercion, ISO8601 parsing). These are not
// exercised directly by the golden-vector parity tests, only indirectly
// through the higher-level Score*/ComputeFramePoints functions. No golden
// vectors or math are changed here — this only adds coverage of existing
// behavior.

func TestClamp(t *testing.T) {
	tests := []struct {
		name      string
		v, lo, hi float64
		want      float64
	}{
		{"within range", 0.5, 0, 1, 0.5},
		{"below range", -1, 0, 1, 0},
		{"above range", 2, 0, 1, 1},
		{"exactly lo", 0, 0, 1, 0},
		{"exactly hi", 1, 0, 1, 1},
		{"negative range", -5, -10, -1, -5},
		{"lo equals hi clamps to that value", 5, 3, 3, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := clamp(tt.v, tt.lo, tt.hi); got != tt.want {
				t.Fatalf("clamp(%v, %v, %v) = %v, want %v", tt.v, tt.lo, tt.hi, got, tt.want)
			}
		})
	}
}

func TestParseISO8601(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		wantOK bool
	}{
		{"empty", "", false},
		{"whitespace only", "   ", false},
		{"malformed", "not-a-date", false},
		{"valid Z suffix", "2026-01-02T03:04:05Z", true},
		{"valid explicit offset", "2026-01-02T03:04:05+00:00", true},
		{"date only rejected", "2026-01-02", false},
		{"valid with whitespace padding", "  2026-01-02T03:04:05Z  ", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseISO8601(tt.in)
			if ok != tt.wantOK {
				t.Fatalf("parseISO8601(%q) ok=%v, want %v (got=%v)", tt.in, ok, tt.wantOK, got)
			}
		})
	}
}

func TestParseISO8601ZAndOffsetAgree(t *testing.T) {
	z, okZ := parseISO8601("2026-06-15T12:00:00Z")
	off, okOff := parseISO8601("2026-06-15T12:00:00+00:00")
	if !okZ || !okOff {
		t.Fatalf("expected both to parse: okZ=%v okOff=%v", okZ, okOff)
	}
	if !z.Equal(off) {
		t.Fatalf("Z and +00:00 suffix should parse to the same instant: %v vs %v", z, off)
	}
	if z.Location() != time.UTC {
		t.Fatalf("expected UTC location, got %v", z.Location())
	}
}

func TestFloatFromAny(t *testing.T) {
	tests := []struct {
		name   string
		in     any
		want   float64
		wantOK bool
	}{
		{"float64", 3.5, 3.5, true},
		{"float32", float32(2.5), 2.5, true},
		{"int", 4, 4.0, true},
		{"int64", int64(6), 6.0, true},
		{"string rejected", "5", 0, false},
		{"nil rejected", nil, 0, false},
		{"bool rejected", true, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := floatFromAny(tt.in)
			if ok != tt.wantOK {
				t.Fatalf("floatFromAny(%v) ok=%v, want %v", tt.in, ok, tt.wantOK)
			}
			if ok && got != tt.want {
				t.Fatalf("floatFromAny(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestStringFromAny(t *testing.T) {
	if got := stringFromAny(nil); got != "" {
		t.Fatalf("stringFromAny(nil) = %q, want empty", got)
	}
	if got := stringFromAny("hello"); got != "hello" {
		t.Fatalf("stringFromAny(hello) = %q, want hello", got)
	}
	if got := stringFromAny(5); got != "" {
		t.Fatalf("stringFromAny(non-string) = %q, want empty", got)
	}
}

func TestNilIfEmpty(t *testing.T) {
	if got := nilIfEmpty(""); got != nil {
		t.Fatalf("nilIfEmpty(\"\") = %v, want nil", got)
	}
	if got := nilIfEmpty("x"); got != "x" {
		t.Fatalf("nilIfEmpty(x) = %v, want x", got)
	}
}

func TestRoundHalfEvenTiesToEven(t *testing.T) {
	tests := []struct {
		name string
		in   float64
		want float64
	}{
		{"2.5 rounds to 2 (even)", 2.5, 2},
		{"3.5 rounds to 4 (even)", 3.5, 4},
		{"0.5 rounds to 0 (even)", 0.5, 0},
		{"1.5 rounds to 2 (even)", 1.5, 2},
		{"-0.5 rounds to 0 (even, floor-based)", -0.5, 0},
		{"non-tie rounds normally down", 2.3, 2},
		{"non-tie rounds normally up", 2.7, 3},
		{"exact integer unchanged", 5.0, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := roundHalfEven(tt.in); got != tt.want {
				t.Fatalf("roundHalfEven(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestRoundHalfEvenSpecialValues(t *testing.T) {
	if got := roundHalfEven(math.NaN()); !math.IsNaN(got) {
		t.Fatalf("roundHalfEven(NaN) = %v, want NaN", got)
	}
	if got := roundHalfEven(math.Inf(1)); !math.IsInf(got, 1) {
		t.Fatalf("roundHalfEven(+Inf) = %v, want +Inf", got)
	}
	if got := roundHalfEven(math.Inf(-1)); !math.IsInf(got, -1) {
		t.Fatalf("roundHalfEven(-Inf) = %v, want -Inf", got)
	}
}

func TestRoundN(t *testing.T) {
	tests := []struct {
		name    string
		v       float64
		ndigits int
		want    float64
	}{
		{"round4 typical", 0.123456, 4, 0.1235},
		{"round2 typical", 1.005, 2, 1.0}, // IEEE noise means 1.005 is slightly < exact .5 boundary
		{"round0 integer", 3.7, 0, 4},
		{"negative ndigits", 1234.0, -2, 1200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := roundN(tt.v, tt.ndigits); got != tt.want {
				t.Fatalf("roundN(%v, %d) = %v, want %v", tt.v, tt.ndigits, got, tt.want)
			}
		})
	}
}

func TestRound1Round2Round4Round6(t *testing.T) {
	if got := round1(1.23456); got != 1.2 {
		t.Fatalf("round1 = %v, want 1.2", got)
	}
	if got := round2(1.23456); got != 1.23 {
		t.Fatalf("round2 = %v, want 1.23", got)
	}
	if got := round4(1.23456); got != 1.2346 {
		t.Fatalf("round4 = %v, want 1.2346", got)
	}
	if got := round6(1.2345678); got != 1.234568 {
		t.Fatalf("round6 = %v, want 1.234568", got)
	}
}

func TestFloatFromMap(t *testing.T) {
	tests := []struct {
		name string
		m    map[string]any
		key  string
		want float64
	}{
		{"nil map", nil, "x", 0},
		{"missing key", map[string]any{}, "x", 0},
		{"nil value", map[string]any{"x": nil}, "x", 0},
		{"float64 value", map[string]any{"x": 2.5}, "x", 2.5},
		{"int value", map[string]any{"x": 3}, "x", 3.0},
		{"int64 value", map[string]any{"x": int64(4)}, "x", 4.0},
		{"unsupported type", map[string]any{"x": "5"}, "x", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := floatFromMap(tt.m, tt.key); got != tt.want {
				t.Fatalf("floatFromMap(%v, %q) = %v, want %v", tt.m, tt.key, got, tt.want)
			}
		})
	}
}

func TestIntFromMap(t *testing.T) {
	tests := []struct {
		name string
		m    map[string]any
		key  string
		want int
	}{
		{"nil map", nil, "x", 0},
		{"missing key", map[string]any{}, "x", 0},
		{"nil value", map[string]any{"x": nil}, "x", 0},
		{"float64 truncates", map[string]any{"x": 5.9}, "x", 5},
		{"int value", map[string]any{"x": 3}, "x", 3},
		{"int64 value", map[string]any{"x": int64(4)}, "x", 4},
		{"unsupported type", map[string]any{"x": "5"}, "x", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := intFromMap(tt.m, tt.key); got != tt.want {
				t.Fatalf("intFromMap(%v, %q) = %v, want %v", tt.m, tt.key, got, tt.want)
			}
		})
	}
}

func TestFloatPtrFromMap(t *testing.T) {
	if p := floatPtrFromMap(nil, "x"); p != nil {
		t.Fatalf("floatPtrFromMap(nil map) = %v, want nil", p)
	}
	if p := floatPtrFromMap(map[string]any{}, "x"); p != nil {
		t.Fatalf("floatPtrFromMap(missing key) = %v, want nil", p)
	}
	if p := floatPtrFromMap(map[string]any{"x": nil}, "x"); p != nil {
		t.Fatalf("floatPtrFromMap(nil value) = %v, want nil", p)
	}
	if p := floatPtrFromMap(map[string]any{"x": "not a number"}, "x"); p != nil {
		t.Fatalf("floatPtrFromMap(unsupported type) = %v, want nil", p)
	}
	p := floatPtrFromMap(map[string]any{"x": 2.5}, "x")
	if p == nil || *p != 2.5 {
		t.Fatalf("floatPtrFromMap(float64) = %v, want pointer to 2.5", p)
	}
	p = floatPtrFromMap(map[string]any{"x": 7}, "x")
	if p == nil || *p != 7.0 {
		t.Fatalf("floatPtrFromMap(int) = %v, want pointer to 7.0", p)
	}
}
