package shared

import (
	"math"
	"time"
)

// ═══════════════════════════════════════════════════════════════
// Slew-time estimation — used by orchestrator to compute angular
// distance between current mount position and target, then
// estimate slew duration given the mount's slew rate.
// ═══════════════════════════════════════════════════════════════

// ComputeTargetAltAz converts ICRS (RA/Dec) to Alt/Az for a site at given time.
// Returns altitude and azimuth in degrees.
func ComputeTargetAltAz(raDeg, decDeg, latDeg, lonDeg float64, now time.Time) (altDeg, azDeg float64) {
	// Julian-day math consumes UTC clock fields. Normalize the representation
	// first so a caller using local time (as the resident pier agent does)
	// describes the same instant as a caller using UTC.
	now = now.UTC()
	jd := julianDay(now)
	gmstDeg := greenwichMeanSiderealTime(jd)

	// Local Sidereal Time
	lstDeg := math.Mod(gmstDeg+lonDeg, 360.0)

	// Hour Angle
	haDeg := math.Mod(lstDeg-raDeg+360.0, 360.0)

	// Convert to radians
	haRad := haDeg * math.Pi / 180.0
	decRad := decDeg * math.Pi / 180.0
	latRad := latDeg * math.Pi / 180.0

	// Altitude
	sinAlt := math.Sin(decRad)*math.Sin(latRad) +
		math.Cos(decRad)*math.Cos(latRad)*math.Cos(haRad)
	// Clamp to [-1, 1] for arcsin
	if sinAlt > 1.0 {
		sinAlt = 1.0
	}
	if sinAlt < -1.0 {
		sinAlt = -1.0
	}
	altRad := math.Asin(sinAlt)
	altDeg = altRad * 180.0 / math.Pi

	// Azimuth (measured from North through East)
	x := -math.Sin(haRad) * math.Cos(decRad)
	y := math.Sin(decRad)*math.Cos(latRad) - math.Cos(decRad)*math.Sin(latRad)*math.Cos(haRad)
	azRad := math.Atan2(x, y)
	azDeg = math.Mod(azRad*180.0/math.Pi+360.0, 360.0)

	return
}

// AngularDistanceDeg computes the great-circle angular distance (degrees)
// between two points on the celestial sphere using the haversine formula.
// For alt/az coordinates, this approximates the mount's slew distance.
func AngularDistanceDeg(alt1Deg, az1Deg, alt2Deg, az2Deg float64) float64 {
	dAlt := (alt2Deg - alt1Deg) * math.Pi / 180.0
	dAz := (az2Deg - az1Deg) * math.Pi / 180.0
	alt1Rad := alt1Deg * math.Pi / 180.0
	alt2Rad := alt2Deg * math.Pi / 180.0

	a := math.Sin(dAlt/2.0)*math.Sin(dAlt/2.0) +
		math.Cos(alt1Rad)*math.Cos(alt2Rad)*math.Sin(dAz/2.0)*math.Sin(dAz/2.0)
	if a > 1.0 {
		a = 1.0
	}
	if a < 0.0 {
		a = 0.0
	}
	c := 2.0 * math.Atan2(math.Sqrt(a), math.Sqrt(1.0-a))
	return c * 180.0 / math.Pi
}

// EstimateSlewTimeMs estimates the slew time in milliseconds from the
// current mount position to the target position, given the mount slew rate.
// Returns 0 if slewRateDegS is nil or <= 0 (unknown rate).
func EstimateSlewTimeMs(currentAltDeg, currentAzDeg, targetAltDeg, targetAzDeg float64, slewRateDegS *float64) int {
	if slewRateDegS == nil || *slewRateDegS <= 0 {
		return 0
	}
	dist := AngularDistanceDeg(currentAltDeg, currentAzDeg, targetAltDeg, targetAzDeg)
	timeSec := dist / *slewRateDegS
	return int(timeSec * 1000)
}

// ─────────────────────────────────────────────────────────────
// Julian Day / GMST — simplified but sufficient for
// sub-degree slew estimation accuracy.
// ─────────────────────────────────────────────────────────────

func julianDay(t time.Time) float64 {
	y, m, d := t.Date()
	h := float64(t.Hour()) + float64(t.Minute())/60.0 + float64(t.Second())/3600.0 +
		float64(t.Nanosecond())/3.6e12

	if m <= 2 {
		y--
		m += 12
	}

	// Gregorian calendar correction
	a := math.Floor(float64(y) / 100.0)
	b := 2.0 - a + math.Floor(a/4.0)

	jd := math.Floor(365.25*float64(y+4716)) +
		math.Floor(30.6001*float64(m+1)) +
		float64(d) + h/24.0 + b - 1524.5
	return jd
}

func greenwichMeanSiderealTime(jd float64) float64 {
	t := (jd - 2451545.0) / 36525.0 // Julian centuries since J2000
	gmst := 280.46061837 +
		360.98564736629*(jd-2451545.0) +
		0.000387933*t*t -
		t*t*t/38710000.0
	return math.Mod(gmst, 360.0)
}
