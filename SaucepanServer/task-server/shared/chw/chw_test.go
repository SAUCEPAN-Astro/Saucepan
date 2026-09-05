package chw

import (
	"math"
	"testing"
)

func TestCHw(t *testing.T) {
	tests := []struct {
		name     string
		aperture float64
		qe       float64
		want     float64
	}{
		{"2m reference", 2000, 1.0, 1.0},
		{"200mm", 200, 1.0, 0.01},
		{"400mm", 400, 1.0, 0.04},
		{"half QE", 2000, 0.5, 0.5},
		{"unknown QE", 2000, 0, 1.0},
		{"zero aperture", 0, 1.0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CHw(tc.aperture, tc.qe)
			if math.Abs(got-tc.want) > 1e-12 {
				t.Fatalf("CHw(%v,%v)=%v want %v", tc.aperture, tc.qe, got, tc.want)
			}
		})
	}
}
