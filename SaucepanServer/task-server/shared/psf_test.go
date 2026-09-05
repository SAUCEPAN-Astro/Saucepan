package shared

import "testing"

func TestDiffractionLimitArcsec_zeroAperture(t *testing.T) {
	if DiffractionLimitArcsec(0) != 0 {
		t.Fatal("expected 0 for zero aperture")
	}
	if DiffractionLimitArcsec(-10) != 0 {
		t.Fatal("expected 0 for negative aperture")
	}
}

func TestPredictedPSFArcsec_seeingLimited(t *testing.T) {
	got := PredictedPSFArcsec(400, 2.5)
	if got != 2.5 {
		t.Fatalf("expected seeing-limited 2.5, got %v", got)
	}
}

func TestPredictedPSFArcsec_diffractionLimited(t *testing.T) {
	got := PredictedPSFArcsec(600, 0.1)
	want := DiffractionLimitArcsec(600)
	if got != want {
		t.Fatalf("expected diffraction limit %v, got %v", want, got)
	}
}

func TestPlateScaleArcsecPerPx(t *testing.T) {
	got := PlateScaleArcsecPerPx(1000, 4.63)
	if got < 0.95 || got > 0.96 {
		t.Fatalf("expected ~0.955 arcsec/px, got %v", got)
	}
}
