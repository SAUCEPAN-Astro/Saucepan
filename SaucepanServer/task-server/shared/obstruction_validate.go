package shared

import (
	"fmt"
	"math"
)

// ValidateObstructionMask checks client-supplied mask structure (Flask parity).
// Returns nil if valid or empty; error message otherwise.
func ValidateObstructionMask(mask ObstructionMask) error {
	if len(mask) == 0 {
		return nil
	}
	for pi, poly := range mask {
		if len(poly) < 3 {
			return fmt.Errorf("polygon %d must have at least 3 vertices", pi)
		}
		for vi, vert := range poly {
			if len(vert) != 2 {
				return fmt.Errorf("polygon %d vertex %d must be [alt_deg, az_deg]", pi, vi)
			}
			alt := vert[0]
			if alt < -90 || alt > 90 || math.IsNaN(alt) {
				return fmt.Errorf("polygon %d vertex %d: altitude must be in [-90, 90]", pi, vi)
			}
			if math.IsNaN(vert[1]) {
				return fmt.Errorf("polygon %d vertex %d: azimuth must be numeric", pi, vi)
			}
		}
	}
	return nil
}
