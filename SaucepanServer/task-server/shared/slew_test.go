package shared

import (
	"math"
	"testing"
	"time"
)

func TestComputeTargetAltAzUsesAbsoluteInstant(t *testing.T) {
	instant := time.Date(2026, 9, 5, 6, 17, 0, 0, time.UTC)
	local := instant.In(time.FixedZone("IST", 5*60*60+30*60))

	utcAlt, utcAz := ComputeTargetAltAz(153.3931, 0, 28.6139, 77.209, instant)
	localAlt, localAz := ComputeTargetAltAz(153.3931, 0, 28.6139, 77.209, local)
	if math.Abs(utcAlt-localAlt) > 1e-9 || math.Abs(utcAz-localAz) > 1e-9 {
		t.Fatalf("same instant produced different Alt/Az: UTC=(%.9f, %.9f), local=(%.9f, %.9f)", utcAlt, utcAz, localAlt, localAz)
	}
}
