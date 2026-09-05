package shared

import (
	"math"
	"testing"
	"time"
)

func TestValidateMountLimits(t *testing.T) {
	minAlt := 15.0
	maxAlt := 85.0
	limits := &MountLimits{}
	limits.Altitude.Min = &minAlt
	limits.Altitude.Max = &maxAlt
	if !ValidateMountLimits(30, 180, limits) {
		t.Fatal("expected inside limits")
	}
	if ValidateMountLimits(10, 180, limits) {
		t.Fatal("expected below min alt")
	}
}

func TestValidateMountLimitsAzimuthFullTurn(t *testing.T) {
	limits := &MountLimits{}
	limits.Azimuth.Min = floatPtrTest(0)
	limits.Azimuth.Max = floatPtrTest(360)
	for _, az := range []float64{0, 45, 180, 359.9, 720, -45} {
		if !ValidateMountLimits(30, az, limits) {
			t.Errorf("azimuth %v should pass a 0..360 full-turn range", az)
		}
	}
}

func TestValidateMountLimitsAzimuthWraps(t *testing.T) {
	limits := &MountLimits{}
	limits.Azimuth.Min = floatPtrTest(300)
	limits.Azimuth.Max = floatPtrTest(30)
	for _, tc := range []struct {
		az   float64
		want bool
	}{
		{az: 315, want: true},
		{az: 15, want: true},
		{az: 180, want: false},
	} {
		if got := ValidateMountLimits(30, tc.az, limits); got != tc.want {
			t.Errorf("azimuth %v = %v, want %v for wrapped range", tc.az, got, tc.want)
		}
	}
}

func TestPassesAltAzSafetyAllowsConfiguredFullTurn(t *testing.T) {
	lat, lon := 28.6139, 77.209
	limits := &MountLimits{}
	limits.Altitude.Min = floatPtrTest(10)
	limits.Altitude.Max = floatPtrTest(85)
	limits.Azimuth.Min = floatPtrTest(0)
	limits.Azimuth.Max = floatPtrTest(360)
	now := time.Date(2026, 9, 5, 6, 8, 0, 0, time.UTC)
	// Pick the local meridian so the target is unambiguously above the
	// altitude floor while exercising the exact safety-file shape used by the
	// mock Alpaca example.
	ra := math.Mod(greenwichMeanSiderealTime(julianDay(now))+lon, 360)
	if !PassesAltAzSafety(ra, 0, nil, TelescopeSafety{
		MountLimits: limits, SiteLat: &lat, SiteLon: &lon,
	}, now) {
		t.Fatalf("meridian target RA=%.4f should pass a 10..85 altitude and 0..360 azimuth envelope", ra)
	}
}

func floatPtrTest(v float64) *float64 { return &v }

func TestAboveHorizonProfile(t *testing.T) {
	profile := &HorizonProfile{
		Points: []struct {
			Az  float64 `json:"az"`
			Alt float64 `json:"alt"`
		}{
			{Az: 0, Alt: 10},
			{Az: 90, Alt: 35},
			{Az: 180, Alt: 10},
		},
	}
	if !AboveHorizonProfile(40, 90, profile) {
		t.Fatal("expected above horizon at az=90")
	}
	if AboveHorizonProfile(20, 90, profile) {
		t.Fatal("expected below horizon at az=90")
	}
}

func TestPassesAltAzSafetyFailsClosedWhenSiteUnset(t *testing.T) {
	if PassesAltAzSafety(180, 45, nil, TelescopeSafety{}, time.Now().UTC()) {
		t.Fatal("missing site coordinates must fail closed (reject), not pass open")
	}
}

func TestPassesAltAzSafetyFailsClosedWhenLatNilOnly(t *testing.T) {
	lon := 10.0
	safety := TelescopeSafety{SiteLon: &lon}
	if PassesAltAzSafety(180, 45, nil, safety, time.Now().UTC()) {
		t.Fatal("nil SiteLat alone must fail closed")
	}
}

func TestPassesAltAzSafetyFailsClosedWhenLonNilOnly(t *testing.T) {
	lat := 51.0
	safety := TelescopeSafety{SiteLat: &lat}
	if PassesAltAzSafety(180, 45, nil, safety, time.Now().UTC()) {
		t.Fatal("nil SiteLon alone must fail closed")
	}
}

func TestPassesAltAzSafetyPassesWhenSitePresentAndValid(t *testing.T) {
	lat, lon := 51.5, -0.1
	safety := TelescopeSafety{SiteLat: &lat, SiteLon: &lon}
	// RA/Dec/time chosen so the target is well above any default horizon;
	// with no MountLimits/HorizonProfile/ObstructionMask configured, the only
	// gate left is the site-coord presence check itself.
	if !PassesAltAzSafety(180, 45, nil, safety, time.Now().UTC()) {
		t.Fatal("present, valid site coords with no configured sub-checks should still pass")
	}
}

func TestPassesAltAzSafetyZeroZeroIsAValidSite(t *testing.T) {
	// (0,0) is a real site (Gulf of Guinea) once coordinates are represented
	// as pointers — it must not be treated as "unset" (#405 fixed the old
	// float64 sentinel; this guards the regression). With no MountLimits,
	// HorizonProfile, or ObstructionMask configured, PassesAltAzSafety with a
	// real (0,0) site must reach and pass the same "no sub-checks configured"
	// path as any other explicit site — proven by matching the
	// present-and-valid case's result for the same RA/Dec/time.
	lat, lon := 0.0, 0.0
	safety := TelescopeSafety{SiteLat: &lat, SiteLon: &lon}
	now := time.Now().UTC()
	if !PassesAltAzSafety(180, 45, nil, safety, now) {
		t.Fatal("explicit (0,0) site coords must be evaluated, not treated as unset")
	}
}

func TestPointInForbiddenMatchesPython(t *testing.T) {
	mask := ObstructionMask{{{30, 0}, {30, 90}, {10, 90}, {10, 0}}}
	if !PointInForbiddenAltAz(20, 45, mask) {
		t.Fatal("expected inside forbidden")
	}
	if PointInForbiddenAltAz(40, 45, mask) {
		t.Fatal("expected outside forbidden")
	}
}

func TestMalformedObstructionGeometryFailsClosed(t *testing.T) {
	malformed := ObstructionMask{{{30, 0}, {30}}}
	if PointInForbiddenAltAz(20, 45, malformed) {
		t.Fatal("malformed obstruction geometry must not match")
	}
	if SlewPathHitsForbidden(20, 45, 25, 50, malformed, 4) {
		t.Fatal("malformed obstruction geometry must not match during slew")
	}
	if PointInPolygon(20, 45, [][]float64{{30, 0}, {30}, {10, 90}}) {
		t.Fatal("malformed polygon must not match")
	}
}
