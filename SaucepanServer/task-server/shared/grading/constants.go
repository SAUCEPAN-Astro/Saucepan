// Package grading — pure scoring math ported from compute-server/grading/ (Python SSOT).
// Keep constants in sync with grading/constants.py; parity gated by
// SaucepanServer/contracts/grading/ (constants, dimension, points, and reputation vectors).
package grading

const GraderVersion = "1.0.0-go"

const (
	BasePoints              = 10.0
	ExptimeCapSeconds       = 60.0
	TenureLogScale          = 0.05
	StackEligibleMinQuality = 0.3
	ReliabilityEMAAlpha     = 0.15
	HeadlineEMAAlpha        = 0.10

	SNRFullCredit             = 50.0
	SaturationPenaltyFraction = 0.001
	NeutralFWHMScore          = 0.5
	FilterAbsentScore         = 0.7
	CalibrationBonus          = 0.1

	CaptureLatencyFullSec   = 1800.0
	CaptureLatencyZeroSec   = 14400.0
	UploadDurationFullSec   = 300.0
	UploadDurationZeroSec   = 3600.0
	TimelinessCaptureWeight = 0.6
	TimelinessUploadWeight  = 0.4
	MissingTimelinessScore  = 0.5
)

var CheapDimensionWeights = map[string]float64{
	"image_quality": 0.40,
	"task_fidelity": 0.35,
	"timeliness":    0.25,
}

var ImageQualityWeights = map[string]float64{
	"snr":        0.45,
	"saturation": 0.15,
	"fwhm":       0.40,
}

var CalibratedStatuses = map[string]struct{}{
	"B": {}, "D": {}, "F": {}, "BDF": {}, "BD": {}, "BF": {}, "DF": {},
}
