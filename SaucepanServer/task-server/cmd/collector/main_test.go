package main

import (
	"testing"

	"github.com/saucepan/hotpath/shared"
)

func TestEstimateStartupMS(t *testing.T) {
	cases := []struct {
		status string
		want   int
	}{
		{"idle", 100},
		{"observing", 800},
		{"uploading", 500},
		{"error", 5000},
		{"slewing", 1000},    // default bucket
		{"processing", 1000}, // default bucket
		{"", 1000},           // default bucket
		{"unknown-status", 1000},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			tel := shared.Telemetry{Status: tc.status}
			if got := estimateStartupMS(tel); got != tc.want {
				t.Fatalf("estimateStartupMS(%q) = %d, want %d", tc.status, got, tc.want)
			}
		})
	}
}

func TestTelemetryNodeStatus(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"idle", shared.NodeStatusIdle},
		{"slewing", shared.NodeStatusBusy},
		{"observing", shared.NodeStatusBusy},
		{"uploading", shared.NodeStatusBusy},
		{"processing", shared.NodeStatusBusy},
		{"error", "error"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := telemetryNodeStatus(tc.in)
			if !ok || got != tc.want {
				t.Fatalf("telemetryNodeStatus(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
	for _, status := range []string{"", "garbage-status"} {
		if got, ok := telemetryNodeStatus(status); ok || got != "" {
			t.Fatalf("telemetryNodeStatus(%q) = (%q, %v), want invalid", status, got, ok)
		}
	}
}

func TestEnvCollector(t *testing.T) {
	t.Setenv("SP_COLLECTOR_TEST_KEY", "val")
	if got := env("SP_COLLECTOR_TEST_KEY", "fallback"); got != "val" {
		t.Fatalf("env() = %q, want %q", got, "val")
	}
	if got := env("SP_COLLECTOR_TEST_KEY_UNSET", "fallback"); got != "fallback" {
		t.Fatalf("env() unset = %q, want %q", got, "fallback")
	}
}
