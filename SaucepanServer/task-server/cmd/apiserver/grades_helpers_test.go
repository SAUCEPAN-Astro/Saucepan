package main

import (
	"encoding/json"
	"testing"
)

func TestStrFromAny(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want string
	}{
		{"string", "hello", "hello"},
		{"nil", nil, ""},
		{"int not a string", 5, ""},
		{"float not a string", 5.5, ""},
		{"bool not a string", true, ""},
		{"empty string", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := strFromAny(tt.in); got != tt.want {
				t.Fatalf("strFromAny(%v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestApiFloatFromAny(t *testing.T) {
	tests := []struct {
		name   string
		in     any
		want   float64
		wantOK bool
	}{
		{"float64", 3.14, 3.14, true},
		{"int", 5, 5.0, true},
		{"int64", int64(7), 7.0, true},
		{"json.Number valid", json.Number("2.5"), 2.5, true},
		{"json.Number invalid", json.Number("not-a-number"), 0, false},
		{"string not numeric", "5", 0, false},
		{"nil", nil, 0, false},
		{"bool", true, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := apiFloatFromAny(tt.in)
			if ok != tt.wantOK {
				t.Fatalf("apiFloatFromAny(%v) ok=%v, want %v", tt.in, ok, tt.wantOK)
			}
			if ok && got != tt.want {
				t.Fatalf("apiFloatFromAny(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestFloatPtrFromAny(t *testing.T) {
	if p := floatPtrFromAny(nil); p != nil {
		t.Fatalf("floatPtrFromAny(nil) = %v, want nil", p)
	}
	if p := floatPtrFromAny("not a number"); p != nil {
		t.Fatalf("floatPtrFromAny(non-numeric) = %v, want nil", p)
	}
	p := floatPtrFromAny(42.0)
	if p == nil || *p != 42.0 {
		t.Fatalf("floatPtrFromAny(42.0) = %v, want pointer to 42.0", p)
	}
	// int input should also produce a valid pointer via apiFloatFromAny.
	p = floatPtrFromAny(10)
	if p == nil || *p != 10.0 {
		t.Fatalf("floatPtrFromAny(10) = %v, want pointer to 10.0", p)
	}
}

func TestIntFromAnyOK(t *testing.T) {
	tests := []struct {
		name   string
		in     any
		want   int
		wantOK bool
	}{
		{"float64 truncates", 5.9, 5, true},
		{"int", 5, 5, true},
		{"int64", int64(9), 9, true},
		{"json.Number valid", json.Number("42"), 42, true},
		{"json.Number invalid", json.Number("abc"), 0, false},
		{"string", "5", 0, false},
		{"nil", nil, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := intFromAnyOK(tt.in)
			if ok != tt.wantOK {
				t.Fatalf("intFromAnyOK(%v) ok=%v, want %v", tt.in, ok, tt.wantOK)
			}
			if ok && got != tt.want {
				t.Fatalf("intFromAnyOK(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestIntFromAny(t *testing.T) {
	if got := intFromAny(nil); got != 0 {
		t.Fatalf("intFromAny(nil) = %d, want 0", got)
	}
	if got := intFromAny(7.0); got != 7 {
		t.Fatalf("intFromAny(7.0) = %d, want 7", got)
	}
	if got := intFromAny("not a number"); got != 0 {
		t.Fatalf("intFromAny(string) = %d, want 0", got)
	}
}

func TestMaxHelper(t *testing.T) {
	tests := []struct {
		a, b, want float64
	}{
		{1, 2, 2},
		{2, 1, 2},
		{-1, -2, -1},
		{0, 0, 0},
		{5, 5, 5},
	}
	for _, tt := range tests {
		if got := max(tt.a, tt.b); got != tt.want {
			t.Fatalf("max(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestTruthyAny(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want bool
	}{
		{"bool true", true, true},
		{"bool false", false, false},
		{"nil", nil, false},
		{"float64 nonzero", 1.0, true},
		{"float64 zero", 0.0, false},
		{"int nonzero", 3, true},
		{"int zero", 0, false},
		{"int64 nonzero", int64(3), true},
		{"json.Number nonzero", json.Number("1"), true},
		{"json.Number zero", json.Number("0"), false},
		{"json.Number invalid", json.Number("abc"), false},
		{"string 1", "1", true},
		{"string true", "true", true},
		{"string t", "t", true},
		{"string yes", "yes", true},
		{"string TRUE uppercase", "TRUE", true},
		{"string  padded true ", "  true  ", true},
		{"string 0", "0", false},
		{"string false", "false", false},
		{"string other", "maybe", false},
		{"string empty", "", false},
		{"unsupported type slice", []int{1}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := truthyAny(tt.in); got != tt.want {
				t.Fatalf("truthyAny(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestResolveOAExptimeDirectValueWins(t *testing.T) {
	data := map[string]any{"sp_exptime": 42.0}
	dimensions := map[string]any{
		"task_fidelity": map[string]any{"exptime_ratio": 0.1},
	}
	if got := resolveOAExptime(data, dimensions); got != 42.0 {
		t.Fatalf("expected direct sp_exptime to win, got %v", got)
	}
}

func TestResolveOAExptimeMissingFidelityYieldsZero(t *testing.T) {
	if got := resolveOAExptime(map[string]any{}, map[string]any{}); got != 0 {
		t.Fatalf("expected 0 when no sp_exptime and no fidelity, got %v", got)
	}
}

func TestResolveOAExptimeZeroRequestedYieldsZero(t *testing.T) {
	data := map[string]any{"integration_time_requested": 0.0}
	dimensions := map[string]any{
		"task_fidelity": map[string]any{"exptime_ratio": 0.5},
	}
	if got := resolveOAExptime(data, dimensions); got != 0 {
		t.Fatalf("expected 0 when requested integration time is 0, got %v", got)
	}
}

func TestResolveOAExptimeMalformedFidelityYieldsZero(t *testing.T) {
	data := map[string]any{"integration_time_requested": 60.0}
	dimensions := map[string]any{
		"task_fidelity": "not-a-map",
	}
	if got := resolveOAExptime(data, dimensions); got != 0 {
		t.Fatalf("expected 0 for malformed task_fidelity, got %v", got)
	}
}
