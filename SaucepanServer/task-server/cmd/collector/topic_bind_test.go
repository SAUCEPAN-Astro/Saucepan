package main

import "testing"

func TestBindTopicNodeID(t *testing.T) {
	id, ok := bindTopicNodeID("/telemetry/victim", "/telemetry/", "victim")
	if !ok || id != "victim" {
		t.Fatalf("got id=%q ok=%v", id, ok)
	}
	_, ok = bindTopicNodeID("/telemetry/victim", "/telemetry/", "attacker")
	if ok {
		t.Fatal("JSON node_id mismatch must reject")
	}
	id, ok = bindTopicNodeID("/telemetry/victim", "/telemetry/", "")
	if !ok || id != "victim" {
		t.Fatalf("empty JSON node_id should use topic: id=%q ok=%v", id, ok)
	}
}

func TestSiteCoordJumpTooLarge(t *testing.T) {
	// Same point
	if siteCoordJumpTooLarge(28.6, 77.2, 28.6, 77.2, 5) {
		t.Fatal("same coords should pass")
	}
	// ~10 deg jump should fail 5 deg threshold
	if !siteCoordJumpTooLarge(0, 0, 0, 10, 5) {
		t.Fatal("10deg lon jump should reject")
	}
}
