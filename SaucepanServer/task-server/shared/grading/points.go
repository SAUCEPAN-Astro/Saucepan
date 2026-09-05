package grading

import (
	"math"
	"time"
)

// PointsBreakdown mirrors grading.points.compute_frame_points output.
type PointsBreakdown struct {
	BasePoints         float64 `json:"base_points"`
	QualityMultiplier  float64 `json:"quality_multiplier"`
	ExptimeFactor      float64 `json:"exptime_factor"`
	TimelinessFactor   float64 `json:"timeliness_factor"`
	TenureMultiplier   float64 `json:"tenure_multiplier"`
	CampaignMultiplier float64 `json:"campaign_multiplier"`
	OAExptime          float64 `json:"sp_exptime"`
	PointsEarned       float64 `json:"points_earned"`
}

// ReputationPartial mirrors grading.points.build_reputation_partial output.
type ReputationPartial map[string]any

func EMAUpdate(previous *float64, sample, alpha float64) float64 {
	if previous == nil {
		return round4(sample)
	}
	return round4((1.0-alpha)*(*previous) + alpha*sample)
}

// ComputeFramePoints calculates cumulative points for one graded frame.
func ComputeFramePoints(grade map[string]any, telescopeStats map[string]any, campaignMultiplier float64) PointsBreakdown {
	if campaignMultiplier <= 0 {
		campaignMultiplier = 1.0
	}
	dims, _ := grade["dimensions"].(map[string]any)
	imageQuality := DimScore(dims, "image_quality")
	timeliness := DimScore(dims, "timeliness")

	qualityMultiplier := 0.5 + 0.5*imageQuality
	exptime := floatFromMap(grade, "sp_exptime")
	var exptimeFactor float64
	if exptime > 0 {
		exptimeFactor = math.Min(1.0, exptime/ExptimeCapSeconds)
	} else {
		exptimeFactor = 0.25
	}
	timelinessFactor := 0.5 + 0.5*timeliness

	totalExposure := floatFromMap(telescopeStats, "total_exposure_seconds")
	totalHours := math.Max(0, totalExposure) / 3600.0
	tenureMultiplier := 1.0 + math.Log1p(totalHours)*TenureLogScale

	points := BasePoints * qualityMultiplier * exptimeFactor * timelinessFactor * tenureMultiplier * campaignMultiplier

	return PointsBreakdown{
		BasePoints:         BasePoints,
		QualityMultiplier:  round4(qualityMultiplier),
		ExptimeFactor:      round4(exptimeFactor),
		TimelinessFactor:   round4(timelinessFactor),
		TenureMultiplier:   round4(tenureMultiplier),
		CampaignMultiplier: round4(campaignMultiplier),
		OAExptime:          exptime,
		PointsEarned:       round2(points),
	}
}

// BuildReputationPartial merges grade into telescope reputation_stats partial update.
func BuildReputationPartial(
	existing map[string]any,
	headline int,
	dimensions map[string]any,
	pointsEarned, oaExptime float64,
) ReputationPartial {
	stats := existing
	if stats == nil {
		stats = map[string]any{}
	}

	prevReliability := floatPtrFromMap(stats, "reliability_score")
	prevHeadline := floatPtrFromMap(stats, "task_quality_score")
	imageQuality := DimScore(dimensions, "image_quality")

	totalPoints := floatFromMap(stats, "total_points") + pointsEarned
	frameCount := intFromMap(stats, "frame_count") + 1
	totalExposure := floatFromMap(stats, "total_exposure_seconds") + math.Max(0, oaExptime)

	var pointsPerHour any
	if totalExposure > 0 {
		pointsPerHour = round2(totalPoints / (totalExposure / 3600.0))
	}

	return ReputationPartial{
		"total_points":           round2(totalPoints),
		"frame_count":            frameCount,
		"total_exposure_seconds": round1(totalExposure),
		"points_per_hour":        pointsPerHour,
		"reliability_score":      EMAUpdate(prevReliability, imageQuality, ReliabilityEMAAlpha),
		"task_quality_score":     EMAUpdate(prevHeadline, float64(headline)/100.0, HeadlineEMAAlpha),
		"last_ingested_at":       time.Now().UTC().Format(time.RFC3339),
		"source":                 "grade_ingest",
	}
}

// IsStackEligible returns true when image_quality meets the stack threshold.
func IsStackEligible(dimensions map[string]any) bool {
	return DimScore(dimensions, "image_quality") >= StackEligibleMinQuality
}

func MergeReputationStats(existing, partial map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range existing {
		out[k] = v
	}
	for k, v := range partial {
		if v == nil {
			delete(out, k)
		} else {
			out[k] = v
		}
	}
	return out
}

func floatFromMap(m map[string]any, key string) float64 {
	if m == nil {
		return 0
	}
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	default:
		return 0
	}
}

func intFromMap(m map[string]any, key string) int {
	if m == nil {
		return 0
	}
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	default:
		return 0
	}
}

func floatPtrFromMap(m map[string]any, key string) *float64 {
	if m == nil {
		return nil
	}
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	switch n := v.(type) {
	case float64:
		return &n
	case int:
		f := float64(n)
		return &f
	default:
		return nil
	}
}

// roundHalfEven mirrors Python 3 round(): nearest, ties to even.
// tieEps absorbs IEEE noise around exact .5 so ingest points match Python SSOT.
func roundHalfEven(x float64) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return x
	}
	floor := math.Floor(x)
	diff := x - floor
	const tieEps = 1e-9
	if diff > 0.5+tieEps {
		return floor + 1
	}
	if diff < 0.5-tieEps {
		return floor
	}
	if math.Mod(floor, 2) == 0 {
		return floor
	}
	return floor + 1
}

func roundN(v float64, ndigits int) float64 {
	p := math.Pow(10, float64(ndigits))
	return roundHalfEven(v*p) / p
}

func round2(v float64) float64 { return roundN(v, 2) }
