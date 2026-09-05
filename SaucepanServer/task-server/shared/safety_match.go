package shared

import (
	"encoding/json"
	"math"
	"strings"
	"time"
)

// MountLimits and HorizonProfile type declarations moved to
// shared/wire/models.go (#457) and are re-exported here as type aliases by
// shared/wire_alias.go — this file keeps ValidateMountLimits,
// AboveHorizonProfile, and the rest of the scheduling logic.

// TelescopeSafety holds persisted safety data for scheduling.
type TelescopeSafety struct {
	ObstructionMask ObstructionMask
	MountLimits     *MountLimits
	HorizonProfile  *HorizonProfile
	SiteLat         *float64
	SiteLon         *float64
	MountAltDeg     *float64
	MountAzDeg      *float64
}

func ValidateMountLimits(altDeg, azDeg float64, limits *MountLimits) bool {
	if limits == nil {
		return true
	}
	if limits.Altitude.Min != nil && altDeg < *limits.Altitude.Min {
		return false
	}
	if limits.Altitude.Max != nil && altDeg > *limits.Altitude.Max {
		return false
	}
	az := normalizeAzimuth(azDeg)
	if limits.Azimuth.Min != nil && limits.Azimuth.Max != nil {
		// A 0..360 interval is a full turn, not a zero-width interval after
		// modulo reduction. Preserve an exact 360 upper bound for the same
		// reason, so a single max=360 bound remains unrestricted.
		if *limits.Azimuth.Max-*limits.Azimuth.Min >= 360.0 {
			return true
		}
		lo := normalizeAzimuthBound(*limits.Azimuth.Min)
		hi := normalizeAzimuthBound(*limits.Azimuth.Max)
		if lo <= hi {
			if az < lo || az > hi {
				return false
			}
		} else if az > hi && az < lo {
			return false
		}
	} else {
		if limits.Azimuth.Min != nil && az < normalizeAzimuthBound(*limits.Azimuth.Min) {
			return false
		}
		if limits.Azimuth.Max != nil && az > normalizeAzimuthBound(*limits.Azimuth.Max) {
			return false
		}
	}
	return true
}

func normalizeAzimuth(azDeg float64) float64 {
	az := math.Mod(azDeg, 360.0)
	if az < 0 {
		az += 360.0
	}
	return az
}

func normalizeAzimuthBound(azDeg float64) float64 {
	if azDeg == 360.0 {
		return 360.0
	}
	return normalizeAzimuth(azDeg)
}

func HorizonAltAt(azDeg float64, profile *HorizonProfile) float64 {
	if profile == nil || len(profile.Points) == 0 {
		return -90.0
	}
	az := math.Mod(azDeg, 360.0)
	pts := profile.Points
	if len(pts) == 1 {
		return pts[0].Alt
	}
	for i := range pts {
		j := (i + 1) % len(pts)
		a0, alt0 := math.Mod(pts[i].Az, 360.0), pts[i].Alt
		a1, alt1 := math.Mod(pts[j].Az, 360.0), pts[j].Alt
		if a0 <= a1 {
			if az >= a0 && az <= a1 {
				t := 0.0
				if math.Abs(a1-a0) > 1e-9 {
					t = (az - a0) / (a1 - a0)
				}
				return alt0 + t*(alt1-alt0)
			}
		} else if az >= a0 || az <= a1 {
			span := (360.0 - a0) + a1
			t := (az - a0) / span
			if az <= a1 {
				t = (360.0 - a0 + az) / span
			}
			return alt0 + t*(alt1-alt0)
		}
	}
	return pts[0].Alt
}

func AboveHorizonProfile(altDeg, azDeg float64, profile *HorizonProfile) bool {
	if profile == nil {
		return true
	}
	return altDeg > HorizonAltAt(azDeg, profile)
}

// PassesAltAzSafety runs obstruction checks after task shortlisting.
func PassesAltAzSafety(
	targetRA, targetDec float64,
	minAltitudeDeg *float64,
	safety TelescopeSafety,
	when time.Time,
) bool {
	if safety.SiteLat == nil || safety.SiteLon == nil {
		// Missing location is "cannot evaluate", not "no restrictions" — fail closed.
		return false
	}
	alt, az := ComputeTargetAltAz(targetRA, targetDec, *safety.SiteLat, *safety.SiteLon, when)
	if minAltitudeDeg != nil && alt < *minAltitudeDeg {
		return false
	}
	if !ValidateMountLimits(alt, az, safety.MountLimits) {
		return false
	}
	if !AboveHorizonProfile(alt, az, safety.HorizonProfile) {
		return false
	}
	if len(safety.ObstructionMask) > 0 && PointInForbiddenAltAz(alt, az, safety.ObstructionMask) {
		return false
	}
	if safety.MountAltDeg != nil && safety.MountAzDeg != nil && len(safety.ObstructionMask) > 0 {
		if SlewPathHitsForbidden(*safety.MountAltDeg, *safety.MountAzDeg, alt, az, safety.ObstructionMask, 24) {
			return false
		}
	}
	return true
}

func ParseObstructionMask(raw string) (ObstructionMask, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var mask ObstructionMask
	if err := json.Unmarshal([]byte(raw), &mask); err != nil {
		return nil, err
	}
	if err := ValidateObstructionMask(mask); err != nil {
		return nil, err
	}
	return mask, nil
}

func ParseMountLimits(raw string) *MountLimits {
	if raw == "" {
		return nil
	}
	var limits MountLimits
	if json.Unmarshal([]byte(raw), &limits) != nil {
		return nil
	}
	return &limits
}

func ParseHorizonProfile(raw string) *HorizonProfile {
	if raw == "" {
		return nil
	}
	var profile HorizonProfile
	if json.Unmarshal([]byte(raw), &profile) != nil {
		return nil
	}
	return &profile
}
