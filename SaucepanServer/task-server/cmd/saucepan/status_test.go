package main

import (
	"testing"
	"time"

	"github.com/saucepan/hotpath/shared/wire"
)

// TestPresence covers the three cases from §5, the boundary at exactly the
// 60s heartbeat floor, and the case that motivates the whole design: a
// retained /status/ of "offline" must never override telemetry that
// actually arrived. presence() doesn't even accept status_retained as a
// parameter — that omission is the fix for #459.
func TestPresence(t *testing.T) {
	cases := []struct {
		name          string
		telemetrySeen bool
		window        time.Duration
		want          string
	}{
		{"telemetry arrived, short window -> live", true, 2 * time.Second, "live"},
		{"telemetry arrived, long window, despite a stale retained offline -> live", true, 65 * time.Second, "live"},
		{"silence under the 60s floor -> waiting", false, 5 * time.Second, "waiting"},
		{"silence exactly at the 60s floor -> offline", false, wire.TelemetryHeartbeatMax, "offline"},
		{"silence past the 60s floor -> offline", false, 65 * time.Second, "offline"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := presence(tc.telemetrySeen, tc.window); got != tc.want {
				t.Fatalf("presence(%v, %s) = %q, want %q", tc.telemetrySeen, tc.window, got, tc.want)
			}
		})
	}
}
