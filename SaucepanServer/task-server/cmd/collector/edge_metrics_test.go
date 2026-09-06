package main

import (
	"testing"
	"time"

	"github.com/saucepan/hotpath/shared"
)

func TestEdgeObsThrottleOnChangeAndHeartbeat(t *testing.T) {
	th := newEdgeObsThrottle()
	now := time.Now()
	tel := shared.Telemetry{NodeID: "n1", Status: "idle", LoadPct: 10}
	if !th.shouldPublish("n1", edgeDecisionFingerprint(tel, "idle", 100), now) {
		t.Fatal("first publish must go")
	}
	if th.shouldPublish("n1", edgeDecisionFingerprint(tel, "idle", 100), now.Add(time.Second)) {
		t.Fatal("unchanged within heartbeat must not publish")
	}
	tel.Status = "observing"
	if !th.shouldPublish("n1", edgeDecisionFingerprint(tel, "busy", 800), now.Add(2*time.Second)) {
		t.Fatal("status change must publish")
	}
	tel.Status = "observing"
	if !th.shouldPublish("n1", edgeDecisionFingerprint(tel, "busy", 800), now.Add(2*time.Second+edgeObservationHeartbeat+time.Second)) {
		t.Fatal("heartbeat must publish")
	}
}

func TestBuildEdgeOpsMetricsIncludesExtendedFields(t *testing.T) {
	cpu := 11.0
	cam := true
	temp := -10.0
	tel := shared.Telemetry{
		NodeID:         "n1",
		Status:         "observing",
		LoadPct:        11,
		CompletedFiles: 3,
		MemoryAvailMB:  1024,
		CPUPct:         &cpu,
		AlpacaCamConn:  &cam,
		CamTemp:        &temp,
	}
	ops := buildEdgeOpsMetrics(tel, "busy", 800, time.Now().UTC())
	if ops["ops.cpu_pct"] != cpu {
		t.Fatalf("cpu: %v", ops["ops.cpu_pct"])
	}
	if ops["ops.alpaca_cam_conn"] != true {
		t.Fatalf("cam conn: %v", ops["ops.alpaca_cam_conn"])
	}
	if ops["ops.cam_temp"] != temp {
		t.Fatalf("cam temp: %v", ops["ops.cam_temp"])
	}
	if ops["ops.est_startup_ms"] != 800 {
		t.Fatalf("startup: %v", ops["ops.est_startup_ms"])
	}
}
