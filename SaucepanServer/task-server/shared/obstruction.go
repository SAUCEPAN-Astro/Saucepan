package shared

import (
	"math"
)

// ═══════════════════════════════════════════════════════════════
// Obstruction geometry — ported from server/utils/scheduler_physics.py
// and server/utils/slew_geometry.py (pure math, no external deps).
//
// ObstructionMask format (matches Python): list of polygons,
// each polygon is a list of [altDeg, azDeg] points.
// ═══════════════════════════════════════════════════════════════

// ObstructionMask type declaration moved to shared/wire/models.go (#457) and
// is re-exported here as a type alias by shared/wire_alias.go — this file
// keeps the geometry functions, which are scheduler logic, not wire contract.

// PointInPolygon checks if (x, y) is inside the polygon defined by vertices
// using the ray-casting algorithm. vertices: list of [x, y] points.
// Ported from server/utils/scheduler_physics.py point_in_polygon().
func PointInPolygon(x, y float64, vertices [][]float64) bool {
	n := len(vertices)
	if n < 3 {
		return false
	}
	inside := false
	j := n - 1
	for _, vertex := range vertices {
		if len(vertex) < 2 || math.IsNaN(vertex[0]) || math.IsNaN(vertex[1]) ||
			math.IsInf(vertex[0], 0) || math.IsInf(vertex[1], 0) {
			return false
		}
	}
	for i := 0; i < n; i++ {
		xi, yi := vertices[i][0], vertices[i][1]
		xj, yj := vertices[j][0], vertices[j][1]
		if ((yi > y) != (yj > y)) && (x < (xj-xi)*(y-yi)/(yj-yi+1e-15)+xi) {
			inside = !inside
		}
		j = i
	}
	return inside
}

// PointInForbiddenAltAz checks if (altDeg, azDeg) is inside any polygon
// in the obstruction mask. Ported from point_in_forbidden_alt_az().
// Returns false if mask is nil or empty.
func PointInForbiddenAltAz(altDeg, azDeg float64, mask ObstructionMask) bool {
	if len(mask) == 0 {
		return false
	}
	azNorm := math.Mod(azDeg, 360.0)
	for _, poly := range mask {
		if len(poly) < 3 {
			continue
		}
		valid := true
		for _, point := range poly {
			if len(point) < 2 || math.IsNaN(point[0]) || math.IsNaN(point[1]) ||
				math.IsInf(point[0], 0) || math.IsInf(point[1], 0) {
				valid = false
				break
			}
		}
		if !valid {
			continue
		}
		// Convert from [alt, az] format to [az, alt] format for the ray caster
		verts := make([][]float64, len(poly))
		for i, p := range poly {
			verts[i] = []float64{math.Mod(p[1], 360.0), p[0]}
		}
		if PointInPolygon(azNorm, altDeg, verts) {
			return true
		}
	}
	return false
}

// SlewPathHitsForbidden checks if any sample point along the mount path
// from (alt0Deg, az0Deg) to (alt1Deg, az1Deg) lies in a forbidden polygon.
// Ported from server/utils/slew_geometry.py slew_path_hits_forbidden().
// Returns false if mask is nil or empty.
// samples: number of interpolation steps (default 24 if <= 0).
func SlewPathHitsForbidden(alt0Deg, az0Deg, alt1Deg, az1Deg float64, mask ObstructionMask, samples int) bool {
	if len(mask) == 0 {
		return false
	}
	if samples <= 0 {
		samples = 24
	}
	for i := 0; i <= samples; i++ {
		t := float64(i) / float64(samples)
		alt := alt0Deg + t*(alt1Deg-alt0Deg)
		// shortest azimuth delta
		daz := math.Mod(az1Deg-az0Deg+540.0, 360.0) - 180.0
		az := math.Mod(az0Deg+t*daz, 360.0)
		if PointInForbiddenAltAz(alt, az, mask) {
			return true
		}
	}
	return false
}
