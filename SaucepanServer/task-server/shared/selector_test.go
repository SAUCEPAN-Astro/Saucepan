package shared

import (
	"testing"
	"time"
)

func ptrF(v float64) *float64 { return &v }

func testReq(ra, dec *float64, allowEmu bool) TaskRequirements {
	return TaskRequirements{RA: ra, Dec: dec, AllowEmulator: allowEmu}
}

func TestSelectEligibleNodes_rejectsObstruction(t *testing.T) {
	lat, lon := 34.0, -118.0
	now := time.Date(2026, 6, 15, 2, 0, 0, 0, time.UTC)
	mask := ObstructionMask{{{30, 0}, {30, 90}, {10, 90}, {10, 0}}}

	var ra, dec float64
	found := false
	for r := 0.0; r < 360 && !found; r += 10 {
		for d := -60.0; d <= 60; d += 5 {
			alt, az := ComputeTargetAltAz(r, d, lat, lon, now)
			if PointInForbiddenAltAz(alt, az, mask) {
				ra, dec = r, d
				found = true
				break
			}
		}
	}
	if !found {
		t.Fatal("could not find test coordinates in obstruction mask")
	}

	clear := NodeEvaluation{
		NodeID: "clear", Status: NodeStatusIdle,
		SiteLat: &lat, SiteLon: &lon,
		MountAltDeg: ptrF(45), MountAzDeg: ptrF(180),
		MountSlewRateDegS: ptrF(5),
	}
	blocked := NodeEvaluation{
		NodeID: "blocked", Status: NodeStatusIdle,
		SiteLat: &lat, SiteLon: &lon,
		ObstructionMask:   mask,
		MountAltDeg:       ptrF(45),
		MountAzDeg:        ptrF(180),
		MountSlewRateDegS: ptrF(5),
	}

	got := SelectEligibleNodes(
		[]NodeEvaluation{blocked, clear},
		testReq(&ra, &dec, false), now, 8,
	)
	if len(got) != 1 {
		t.Fatalf("expected 1 eligible node, got %d", len(got))
	}
	if got[0].NodeID != "clear" {
		t.Fatalf("expected clear node, got %s", got[0].NodeID)
	}
}

// TestSelectEligibleNodes_excludesNodeWithNilSiteCoords guards #453: a node
// whose site coordinates are unknown must be excluded when the request has a
// target (RA/Dec), not silently pass because the AltAz check was skipped.
func TestSelectEligibleNodes_excludesNodeWithNilSiteCoords(t *testing.T) {
	lat, lon := 34.0, -118.0
	ra, dec := 180.0, 45.0
	now := time.Date(2026, 6, 15, 2, 0, 0, 0, time.UTC)

	known := NodeEvaluation{
		NodeID: "known-site", Status: NodeStatusIdle,
		SiteLat: &lat, SiteLon: &lon,
		MountSlewRateDegS: ptrF(5),
	}
	unknownLat := NodeEvaluation{
		NodeID: "unknown-lat", Status: NodeStatusIdle,
		SiteLat: nil, SiteLon: &lon,
		MountSlewRateDegS: ptrF(5),
	}
	unknownLon := NodeEvaluation{
		NodeID: "unknown-lon", Status: NodeStatusIdle,
		SiteLat: &lat, SiteLon: nil,
		MountSlewRateDegS: ptrF(5),
	}
	unknownBoth := NodeEvaluation{
		NodeID: "unknown-both", Status: NodeStatusIdle,
		SiteLat: nil, SiteLon: nil,
		MountSlewRateDegS: ptrF(5),
	}

	got := SelectEligibleNodes(
		[]NodeEvaluation{known, unknownLat, unknownLon, unknownBoth},
		testReq(&ra, &dec, false), now, 8,
	)
	if len(got) != 1 {
		t.Fatalf("expected only the known-site node to be eligible, got %d: %+v", len(got), got)
	}
	if got[0].NodeID != "known-site" {
		t.Fatalf("expected known-site node, got %s", got[0].NodeID)
	}
}

func TestSelectEligibleNodes_ranksBySlew(t *testing.T) {
	lat, lon := 34.0, -118.0
	ra, dec := 180.0, 45.0
	now := time.Date(2026, 6, 15, 2, 0, 0, 0, time.UTC)

	near := NodeEvaluation{
		NodeID: "near", Status: NodeStatusIdle,
		SiteLat: &lat, SiteLon: &lon,
		MountAltDeg: ptrF(44), MountAzDeg: ptrF(179),
		MountSlewRateDegS: ptrF(10),
	}
	far := NodeEvaluation{
		NodeID: "far", Status: NodeStatusIdle,
		SiteLat: &lat, SiteLon: &lon,
		MountAltDeg: ptrF(10), MountAzDeg: ptrF(90),
		MountSlewRateDegS: ptrF(10),
	}

	got := SelectEligibleNodes(
		[]NodeEvaluation{far, near},
		testReq(&ra, &dec, false), now, 8,
	)
	if len(got) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(got))
	}
	if got[0].NodeID != "near" {
		t.Fatalf("expected near first, got %s (scores %d vs %d)", got[0].NodeID, got[0].Score, got[1].Score)
	}
}

func TestSelectEligibleNodes_respectsLimit(t *testing.T) {
	lat, lon := 34.0, -118.0
	ra, dec := 180.0, 45.0
	now := time.Date(2026, 6, 15, 2, 0, 0, 0, time.UTC)
	var nodes []NodeEvaluation
	for i := 0; i < 5; i++ {
		id := NodeEvaluation{
			NodeID: "n", Status: NodeStatusIdle,
			SiteLat: &lat, SiteLon: &lon,
			MountSlewRateDegS: ptrF(5),
		}
		nodes = append(nodes, id)
		nodes[i].NodeID = string(rune('a' + i))
	}
	got := SelectEligibleNodes(nodes, testReq(&ra, &dec, false), now, 3)
	if len(got) != 3 {
		t.Fatalf("expected limit 3, got %d", len(got))
	}
}

func TestSelectEligibleNodes_emulatorPoolIsolation(t *testing.T) {
	lat, lon := 34.0, -118.0
	ra, dec := 180.0, 45.0
	now := time.Date(2026, 6, 15, 2, 0, 0, 0, time.UTC)
	real := NodeEvaluation{
		NodeID: "real-1", Status: NodeStatusIdle,
		SiteLat: &lat, SiteLon: &lon, MountSlewRateDegS: ptrF(5),
	}
	emu := NodeEvaluation{
		NodeID: "emu_node_001", Status: NodeStatusIdle, IsEmulator: true,
		SiteLat: &lat, SiteLon: &lon, MountSlewRateDegS: ptrF(5),
	}

	prod := SelectEligibleNodes([]NodeEvaluation{real, emu}, testReq(&ra, &dec, false), now, 8)
	if len(prod) != 1 || prod[0].NodeID != "real-1" {
		t.Fatalf("production pool: expected real-1 only, got %+v", prod)
	}

	sandbox := SelectEligibleNodes([]NodeEvaluation{real, emu}, testReq(&ra, &dec, true), now, 8)
	if len(sandbox) != 1 || sandbox[0].NodeID != "emu_node_001" {
		t.Fatalf("sandbox pool: expected emu_node_001 only, got %+v", sandbox)
	}
}

func TestSelectEligibleNodes_apertureGate(t *testing.T) {
	lat, lon := 34.0, -118.0
	ra, dec := 180.0, 45.0
	now := time.Date(2026, 6, 15, 2, 0, 0, 0, time.UTC)
	minAperture := 200.0
	small := NodeEvaluation{
		NodeID: "small", Status: NodeStatusIdle,
		SiteLat: &lat, SiteLon: &lon, MountSlewRateDegS: ptrF(5),
		ApertureMM: ptrF(100),
	}
	large := NodeEvaluation{
		NodeID: "large", Status: NodeStatusIdle,
		SiteLat: &lat, SiteLon: &lon, MountSlewRateDegS: ptrF(5),
		ApertureMM: ptrF(300),
	}
	req := testReq(&ra, &dec, false)
	req.MinApertureMM = &minAperture
	got := SelectEligibleNodes([]NodeEvaluation{small, large}, req, now, 8)
	if len(got) != 1 || got[0].NodeID != "large" {
		t.Fatalf("expected only large aperture node, got %+v", got)
	}
}

func TestSelectEligibleNodes_minPowerGate(t *testing.T) {
	lat, lon := 34.0, -118.0
	ra, dec := 180.0, 45.0
	now := time.Date(2026, 6, 15, 2, 0, 0, 0, time.UTC)
	minPower := 0.8
	weak := NodeEvaluation{
		NodeID: "weak", Status: NodeStatusIdle,
		SiteLat: &lat, SiteLon: &lon, MountSlewRateDegS: ptrF(5), Power: 0.7,
	}
	strong := weak
	strong.NodeID = "strong"
	strong.Power = 0.9
	req := testReq(&ra, &dec, false)
	req.MinPower = &minPower

	got := SelectEligibleNodes([]NodeEvaluation{weak, strong}, req, now, 8)
	if len(got) != 1 || got[0].NodeID != "strong" {
		t.Fatalf("expected only strong node, got %+v", got)
	}
}

func TestSelectEligibleNodes_targetMagnitudeGate(t *testing.T) {
	lat, lon := 34.0, -118.0
	ra, dec := 180.0, 45.0
	now := time.Date(2026, 6, 15, 2, 0, 0, 0, time.UTC)
	targetMagnitude := 17.5
	weakLimit := 16.0
	goodLimit := 18.0
	weak := NodeEvaluation{
		NodeID: "shallow", Status: NodeStatusIdle,
		SiteLat: &lat, SiteLon: &lon, MountSlewRateDegS: ptrF(5), LimitingMagnitude: &weakLimit,
	}
	good := weak
	good.NodeID = "deep"
	good.LimitingMagnitude = &goodLimit
	req := testReq(&ra, &dec, false)
	req.TargetMagnitude = &targetMagnitude

	got := SelectEligibleNodes([]NodeEvaluation{weak, good}, req, now, 8)
	if len(got) != 1 || got[0].NodeID != "deep" {
		t.Fatalf("expected only deep node, got %+v", got)
	}
}
