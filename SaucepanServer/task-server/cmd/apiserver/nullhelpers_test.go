package main

import (
	"encoding/json"
	"testing"
)

func TestNullFloat(t *testing.T) {
	tests := []struct {
		name string
		v    float64
		want any
	}{
		{"zero becomes nil", 0, nil},
		{"negative zero becomes nil", -0.0, nil},
		{"positive value passes through", 1.5, 1.5},
		{"negative value passes through", -1.5, -1.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nullFloat(tt.v); got != tt.want {
				t.Fatalf("nullFloat(%v) = %v, want %v", tt.v, got, tt.want)
			}
		})
	}
}

func TestNullString(t *testing.T) {
	if got := nullString(""); got != nil {
		t.Fatalf("nullString(\"\") = %v, want nil", got)
	}
	if got := nullString("x"); got != "x" {
		t.Fatalf("nullString(\"x\") = %v, want \"x\"", got)
	}
	if got := nullString(" "); got != " " {
		t.Fatalf("nullString(\" \") should pass whitespace through unchanged, got %v", got)
	}
}

func TestNullStringSlice(t *testing.T) {
	if got := nullStringSlice(nil); got != nil {
		t.Fatalf("nullStringSlice(nil) = %v, want nil", got)
	}
	// An empty-but-non-nil slice is NOT nulled — only a nil slice is.
	empty := []string{}
	got := nullStringSlice(empty)
	slice, ok := got.([]string)
	if !ok {
		t.Fatalf("nullStringSlice(empty non-nil) returned %T, want []string", got)
	}
	if len(slice) != 0 {
		t.Fatalf("expected empty slice passthrough, got %v", slice)
	}

	in := []string{"a", "b"}
	got = nullStringSlice(in)
	slice, ok = got.([]string)
	if !ok || len(slice) != 2 || slice[0] != "a" || slice[1] != "b" {
		t.Fatalf("nullStringSlice(%v) = %v, want passthrough", in, got)
	}
}

func TestNullJSON(t *testing.T) {
	if got := nullJSON(nil); got != nil {
		t.Fatalf("nullJSON(nil) = %v, want nil", got)
	}
	if got := nullJSON(json.RawMessage{}); got != nil {
		t.Fatalf("nullJSON(empty RawMessage) = %v, want nil", got)
	}
	raw := json.RawMessage(`{"a":1}`)
	got := nullJSON(raw)
	rm, ok := got.(json.RawMessage)
	if !ok || string(rm) != `{"a":1}` {
		t.Fatalf("nullJSON(%s) = %v, want passthrough", raw, got)
	}
	// "null" as literal JSON bytes is non-empty and must pass through unchanged.
	nullLiteral := json.RawMessage(`null`)
	got = nullJSON(nullLiteral)
	rm, ok = got.(json.RawMessage)
	if !ok || string(rm) != "null" {
		t.Fatalf("nullJSON(literal null) = %v, want passthrough of the literal bytes", got)
	}
}
