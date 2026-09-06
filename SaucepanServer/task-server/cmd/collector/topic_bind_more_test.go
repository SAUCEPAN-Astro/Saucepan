package main

import "testing"

// TestBindTopicNodeIDEdgeCases covers cases beyond the happy path already in
// topic_bind_test.go: a topic that doesn't match the expected prefix at all
// (NodeIDFromTopic returns "").
func TestBindTopicNodeIDEdgeCases(t *testing.T) {
	if id, ok := bindTopicNodeID("/wrong/prefix/victim", "/telemetry/", ""); ok || id != "" {
		t.Fatalf("mismatched prefix: id=%q ok=%v, want \"\"/false", id, ok)
	}
	if id, ok := bindTopicNodeID("", "/telemetry/", ""); ok || id != "" {
		t.Fatalf("empty topic: id=%q ok=%v, want \"\"/false", id, ok)
	}
	// Topic id and JSON id equal, both empty-ish suffix (edge: trailing slash
	// leaves an empty node id, must reject).
	if id, ok := bindTopicNodeID("/telemetry/", "/telemetry/", ""); ok || id != "" {
		t.Fatalf("trailing-slash empty id: id=%q ok=%v, want \"\"/false", id, ok)
	}
}

func TestSiteCoordJumpTooLargeEdgeCases(t *testing.T) {
	// Boundary: exactly at threshold must NOT reject (condition is strictly >).
	// 0,0 -> 0,5 is ~555km, way more than 5deg threshold in km terms, so use
	// a tiny separation instead to probe the boundary in degrees directly:
	// same point should never reject regardless of threshold.
	if siteCoordJumpTooLarge(10, 20, 10, 20, 0.001) {
		t.Fatal("identical coordinates must never reject, regardless of threshold")
	}

	// maxDeg <= 0 falls back to the 5.0 default (documented behavior).
	if siteCoordJumpTooLarge(0, 0, 0, 10, 0) != siteCoordJumpTooLarge(0, 0, 0, 10, 5.0) {
		t.Fatal("maxDeg<=0 should behave identically to the 5.0 default")
	}
	if siteCoordJumpTooLarge(0, 0, 0, 10, -1) != siteCoordJumpTooLarge(0, 0, 0, 10, 5.0) {
		t.Fatal("negative maxDeg should behave identically to the 5.0 default")
	}

	// A jump comfortably under threshold must not reject.
	if siteCoordJumpTooLarge(0, 0, 0.01, 0.01, 5) {
		t.Fatal("a sub-degree jump should not reject a 5deg threshold")
	}

	// Antipodal-ish large jump must reject.
	if !siteCoordJumpTooLarge(0, 0, 45, 90, 5) {
		t.Fatal("a large multi-degree jump should reject a 5deg threshold")
	}

	// Negative-to-negative coordinates (southern/western hemisphere) must
	// still compute a sane great-circle distance.
	if siteCoordJumpTooLarge(-33.9, 151.2, -33.9, 151.2, 5) {
		t.Fatal("identical negative coordinates should not reject")
	}
	if !siteCoordJumpTooLarge(-33.9, 151.2, -34.9, 152.2, 0.5) {
		t.Fatal("a >0.5deg jump in the southern hemisphere should reject a 0.5deg threshold")
	}
}

func TestFormatReject(t *testing.T) {
	got := formatReject("/telemetry/n1", "bad payload")
	want := "reject /telemetry/n1: bad payload"
	if got != want {
		t.Fatalf("formatReject() = %q, want %q", got, want)
	}
	// Empty inputs must not panic and must still produce the fixed shape.
	if got := formatReject("", ""); got != "reject : " {
		t.Fatalf("formatReject(empty) = %q, want %q", got, "reject : ")
	}
}
