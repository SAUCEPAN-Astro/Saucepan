package shared

// DiffractionLimitArcsec — Dawes-limit approximation for a given aperture.
func DiffractionLimitArcsec(apertureMM float64) float64 {
	if apertureMM <= 0 {
		return 0
	}
	return DiffractionLimitConstant / apertureMM
}

// PredictedPSFArcsec — expected delivered FWHM: max(diffraction limit, atmospheric seeing).
func PredictedPSFArcsec(apertureMM, siteSeeingArcsec float64) float64 {
	d := DiffractionLimitArcsec(apertureMM)
	if siteSeeingArcsec > d {
		return siteSeeingArcsec
	}
	return d
}

// PlateScaleArcsecPerPx — pixel scale from focal length + pixel size.
func PlateScaleArcsecPerPx(focalLengthMM, pixelSizeUM float64) float64 {
	if focalLengthMM <= 0 {
		return 0
	}
	return 206.265 * pixelSizeUM / focalLengthMM
}
