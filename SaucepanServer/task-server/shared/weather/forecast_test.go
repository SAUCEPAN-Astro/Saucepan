package weather

import "testing"

func TestEstimateSeeingClearCalm(t *testing.T) {
	s := EstimateSeeing(0, 1, 40)
	if s < 0.5 || s > 1.5 {
		t.Fatalf("clear calm seeing=%v want ~1.2", s)
	}
}

func TestEstimateSeeingCloudyWindyWorse(t *testing.T) {
	clear := EstimateSeeing(0, 0, 40)
	bad := EstimateSeeing(90, 15, 80)
	if !(bad > clear) {
		t.Fatalf("cloudy/windy should be worse: clear=%v bad=%v", clear, bad)
	}
	if bad > 5.0 {
		t.Fatalf("should clamp to 5: %v", bad)
	}
}
