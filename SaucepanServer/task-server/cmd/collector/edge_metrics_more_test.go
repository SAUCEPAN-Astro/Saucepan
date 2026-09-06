package main

import (
	"testing"
	"time"

	"github.com/saucepan/hotpath/shared"
)

// TestEdgeDecisionFingerprintDistinguishesFields covers edgeDecisionFingerprint
// directly: two telemetry snapshots that differ only in one tracked field
// (task id, error string, load pct) must produce different fingerprints,
// while two identical snapshots must match — this is the throttle's whole
// change-detection contract.
func TestEdgeDecisionFingerprintDistinguishesFields(t *testing.T) {
	base := shared.Telemetry{Status: "observing", LoadPct: 42}
	fpBase := edgeDecisionFingerprint(base, "busy", 800)

	if got := edgeDecisionFingerprint(base, "busy", 800); got != fpBase {
		t.Fatalf("identical inputs produced different fingerprints: %q vs %q", got, fpBase)
	}

	taskID := 7
	withTask := base
	withTask.CurrentTaskID = &taskID
	if got := edgeDecisionFingerprint(withTask, "busy", 800); got == fpBase {
		t.Fatal("adding a current_task_id should change the fingerprint")
	}

	errMsg := "boom"
	withErr := base
	withErr.PyLastError = &errMsg
	if got := edgeDecisionFingerprint(withErr, "busy", 800); got == fpBase {
		t.Fatal("adding a py_last_error should change the fingerprint")
	}

	loadChanged := base
	loadChanged.LoadPct = 99
	if got := edgeDecisionFingerprint(loadChanged, "busy", 800); got == fpBase {
		t.Fatal("changing load_pct should change the fingerprint")
	}

	if got := edgeDecisionFingerprint(base, "idle", 800); got == fpBase {
		t.Fatal("changing nodeStatus should change the fingerprint")
	}

	if got := edgeDecisionFingerprint(base, "busy", 100); got == fpBase {
		t.Fatal("changing estStartup should change the fingerprint")
	}
}

// TestBuildEdgeOpsMetricsFallbacks covers the "fallback from core fields"
// block (edge_metrics.go ~150-159): when the extended telem_* fields are
// nil/unset, ops.telem_file_count/telem_last_update_at/cpu_pct must fall
// back to CompletedFiles / now / LoadPct rather than being absent.
func TestBuildEdgeOpsMetricsFallbacks(t *testing.T) {
	now := time.Now().UTC()
	tel := shared.Telemetry{
		NodeID:         "n1",
		Status:         "idle",
		LoadPct:        33,
		CompletedFiles: 12,
		// TelemFileCount, TelemLastUpdateAt, CPUPct all nil/unset.
	}
	ops := buildEdgeOpsMetrics(tel, "idle", 100, now)

	if ops["ops.telem_file_count"] != 12 {
		t.Fatalf("telem_file_count fallback = %v, want CompletedFiles=12", ops["ops.telem_file_count"])
	}
	if ops["ops.telem_last_update_at"] != now.UTC().Format(time.RFC3339) {
		t.Fatalf("telem_last_update_at fallback = %v, want now formatted", ops["ops.telem_last_update_at"])
	}
	if ops["ops.cpu_pct"] != 33.0 {
		t.Fatalf("cpu_pct fallback = %v, want LoadPct=33", ops["ops.cpu_pct"])
	}

	// When the extended fields ARE set, they must win over the fallback.
	fileCount := 999
	cpu := 5.5
	lastUpdate := "2026-01-01T00:00:00Z"
	tel2 := tel
	tel2.TelemFileCount = &fileCount
	tel2.CPUPct = &cpu
	tel2.TelemLastUpdateAt = &lastUpdate
	ops2 := buildEdgeOpsMetrics(tel2, "idle", 100, now)
	if ops2["ops.telem_file_count"] != 999 {
		t.Fatalf("telem_file_count should prefer extended field, got %v", ops2["ops.telem_file_count"])
	}
	if ops2["ops.cpu_pct"] != 5.5 {
		t.Fatalf("cpu_pct should prefer extended field, got %v", ops2["ops.cpu_pct"])
	}
	if ops2["ops.telem_last_update_at"] != lastUpdate {
		t.Fatalf("telem_last_update_at should prefer extended field, got %v", ops2["ops.telem_last_update_at"])
	}
}

// TestBuildEdgeOpsMetricsOmitsNilOptionalFields covers the zero-value input
// edge case: an all-defaults Telemetry must not populate any of the
// optional pointer-guarded keys.
func TestBuildEdgeOpsMetricsOmitsNilOptionalFields(t *testing.T) {
	ops := buildEdgeOpsMetrics(shared.Telemetry{}, "idle", 0, time.Now().UTC())
	for _, key := range []string{
		"ops.current_task_id", "ops.current_task_priority", "ops.mount_alt", "ops.mount_az",
		"ops.mqtt_task_receive_ms", "ops.command_ack_latency_ms", "ops.filter_pos", "ops.focuser_pos",
		"ops.py_last_error", "ops.telem_task_id",
	} {
		if _, ok := ops[key]; ok {
			t.Errorf("unexpected key %q present for zero-value telemetry: %v", key, ops[key])
		}
	}
}

// TestMaybeBuildEdgeObservationThrottleGate covers maybeBuildEdgeObservation
// directly (previously only exercised indirectly): the first call for a node
// must return a non-nil Observation, and an immediate repeat with an
// unchanged fingerprint inside the heartbeat window must return nil.
func TestMaybeBuildEdgeObservationThrottleGate(t *testing.T) {
	throttle := newEdgeObsThrottle()
	now := time.Now()
	tel := shared.Telemetry{NodeID: "n1", Status: "idle", LoadPct: 5}

	obs := maybeBuildEdgeObservation(throttle, tel, "idle", 100, now)
	if obs == nil {
		t.Fatal("first observation for a node should not be throttled")
	}
	if obs.EntityID != "n1" {
		t.Fatalf("EntityID = %q, want %q", obs.EntityID, "n1")
	}
	if obs.Producer != "edge_telemetry" {
		t.Fatalf("Producer = %q, want %q", obs.Producer, "edge_telemetry")
	}

	obs2 := maybeBuildEdgeObservation(throttle, tel, "idle", 100, now.Add(time.Second))
	if obs2 != nil {
		t.Fatal("unchanged telemetry within the heartbeat window should be throttled (nil)")
	}

	obs3 := maybeBuildEdgeObservation(throttle, tel, "idle", 100, now.Add(edgeObservationHeartbeat+time.Second))
	if obs3 == nil {
		t.Fatal("observation past the heartbeat window should publish again")
	}
}
