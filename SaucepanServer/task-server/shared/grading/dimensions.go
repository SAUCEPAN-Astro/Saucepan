package grading

import (
	"math"
	"strings"
	"time"
)

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func DimScore(dimensions map[string]any, key string) float64 {
	raw, ok := dimensions[key]
	if !ok || raw == nil {
		return 0
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return 0
	}
	score, ok := m["score"].(float64)
	if !ok {
		return 0
	}
	return clamp(score, 0, 1)
}

func parseISO8601(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	if strings.HasSuffix(value, "Z") {
		value = value[:len(value)-1] + "+00:00"
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

type QualityMetrics struct {
	SNR             float64
	NoiseADU        float64
	StarPixels      int
	SaturatedPixels int
	Shape           [2]int
}

type SPHeaders struct {
	Exptime *float64
	Filter  string
	FWHM    *float64
	Calstat string
	SNR     *float64
	Qual    *float64
	RA      *float64
	Dec     *float64
	DateObs string
}

// OAHeaders is retained as a compatibility alias for the pre-Saucepan name.
type OAHeaders = SPHeaders

func ScoreImageQuality(metrics QualityMetrics, headers SPHeaders, predictedPSF *float64) map[string]any {
	totalPixels := metrics.Shape[0] * metrics.Shape[1]
	if totalPixels < 1 {
		totalPixels = 1
	}
	satFrac := float64(metrics.SaturatedPixels) / float64(totalPixels)

	snrScore := clamp(metrics.SNR/SNRFullCredit, 0, 1)
	satScore := clamp(1.0-(satFrac/SaturationPenaltyFraction), 0, 1)

	var fwhmScore float64
	fwhmSource := "missing"
	if headers.FWHM != nil && predictedPSF != nil && *predictedPSF > 0 {
		fwhmScore = clamp(*predictedPSF/(*headers.FWHM), 0, 1)
		fwhmSource = "header"
	} else if headers.Qual != nil {
		fwhmScore = clamp(*headers.Qual, 0, 1)
		fwhmSource = "sp_qual_proxy"
	} else if headers.FWHM != nil {
		fwhmScore = NeutralFWHMScore
		// Keep this label aligned with dimensions.score_image_quality in the
		// Python source of truth: a measured FWHM without a comparison target
		// still uses the neutral fallback.
		fwhmSource = "neutral"
	} else {
		fwhmScore = NeutralFWHMScore
	}

	score := clamp(
		ImageQualityWeights["snr"]*snrScore+
			ImageQualityWeights["saturation"]*satScore+
			ImageQualityWeights["fwhm"]*fwhmScore,
		0, 1,
	)

	var fwhmVal any
	if headers.FWHM != nil {
		fwhmVal = *headers.FWHM
	}

	return map[string]any{
		"score":               round4(score),
		"snr":                 metrics.SNR,
		"noise_adu":           metrics.NoiseADU,
		"saturation_fraction": round6(satFrac),
		"fwhm_arcsec":         fwhmVal,
		"fwhm_source":         fwhmSource,
		"star_pixels":         metrics.StarPixels,
	}
}

func ScoreTaskFidelity(headers SPHeaders, taskContext map[string]any) map[string]any {
	var exptimeRatio *float64
	if headers.Exptime != nil {
		if req, ok := floatFromAny(taskContext["integration_time_requested"]); ok && req > 0 {
			r := clamp(*headers.Exptime/req, 0, 1)
			exptimeRatio = &r
		}
	}

	filterRequested := strings.ToUpper(strings.TrimSpace(stringFromAny(taskContext["filter_requested"])))
	filterActual := strings.ToUpper(strings.TrimSpace(headers.Filter))

	var filterMatch *bool
	filterScore := FilterAbsentScore
	if filterRequested != "" && filterActual != "" {
		requestedFilters := make(map[string]struct{})
		for _, requested := range strings.Split(filterRequested, ",") {
			if requested = strings.TrimSpace(requested); requested != "" {
				requestedFilters[requested] = struct{}{}
			}
		}
		_, match := requestedFilters[filterActual]
		filterMatch = &match
		if match {
			filterScore = 1.0
		} else {
			filterScore = 0.0
		}
	}

	calstat := strings.ToUpper(strings.TrimSpace(headers.Calstat))
	if calstat == "" {
		calstat = "NONE"
	}
	calBonus := 0.0
	if _, ok := CalibratedStatuses[calstat]; ok {
		calBonus = CalibrationBonus
	}

	var score float64
	if exptimeRatio != nil {
		score = clamp(0.7*(*exptimeRatio)+0.3*filterScore+calBonus, 0, 1)
	} else {
		score = clamp(0.5*filterScore+0.5+calBonus*0.5, 0, 1)
	}

	out := map[string]any{
		"score":            round4(score),
		"filter_match":     filterMatch,
		"filter_requested": nilIfEmpty(filterRequested),
		"filter_actual":    nilIfEmpty(filterActual),
		"calstat":          calstat,
	}
	if exptimeRatio != nil {
		out["exptime_ratio"] = round4(*exptimeRatio)
	} else {
		out["exptime_ratio"] = nil
	}
	return out
}

func ScoreTimeliness(taskContext map[string]any) map[string]any {
	var captureLatencySec, uploadDurationSec *float64

	assignmentAt, hasAssign := parseISO8601(stringFromAny(taskContext["assignment_sent_at"]))
	uploadAtStr := stringFromAny(taskContext["upload_completed_at"])
	if uploadAtStr == "" {
		uploadAtStr = stringFromAny(taskContext["upload_time"])
	}
	uploadAt, hasUpload := parseISO8601(uploadAtStr)
	uploadStart, hasStart := parseISO8601(stringFromAny(taskContext["upload_started_at"]))

	if hasAssign && hasUpload {
		sec := math.Max(0, uploadAt.Sub(assignmentAt).Seconds())
		captureLatencySec = &sec
	}
	if hasStart && hasUpload {
		sec := math.Max(0, uploadAt.Sub(uploadStart).Seconds())
		uploadDurationSec = &sec
	}

	captureScore := MissingTimelinessScore
	if captureLatencySec != nil {
		span := CaptureLatencyZeroSec - CaptureLatencyFullSec
		captureScore = clamp(1.0-(*captureLatencySec-CaptureLatencyFullSec)/span, 0, 1)
	}

	uploadScore := captureScore
	if uploadDurationSec != nil {
		span := UploadDurationZeroSec - UploadDurationFullSec
		uploadScore = clamp(1.0-(*uploadDurationSec-UploadDurationFullSec)/span, 0, 1)
	}

	score := clamp(
		TimelinessCaptureWeight*captureScore+TimelinessUploadWeight*uploadScore,
		0, 1,
	)

	out := map[string]any{
		"score": round4(score),
	}
	if captureLatencySec != nil {
		out["capture_latency_sec"] = round1(*captureLatencySec)
	} else {
		out["capture_latency_sec"] = nil
	}
	if uploadDurationSec != nil {
		out["upload_duration_sec"] = round1(*uploadDurationSec)
	} else {
		out["upload_duration_sec"] = nil
	}
	return out
}

func HeadlineScore(dimensions map[string]any) int {
	// Fixed order matches Python constants.CHEAP_DIMENSION_WEIGHTS insertion order
	// (map range is nondeterministic and can change float accumulation).
	keys := []string{"image_quality", "task_fidelity", "timeliness"}
	total := 0.0
	for _, key := range keys {
		total += CheapDimensionWeights[key] * DimScore(dimensions, key)
	}
	// Python: int(round(100 * clamp(total))) — banker's rounding at .5
	return int(roundHalfEven(100 * clamp(total, 0, 1)))
}

func round4(v float64) float64 { return roundN(v, 4) }
func round6(v float64) float64 { return roundN(v, 6) }
func round1(v float64) float64 { return roundN(v, 1) }

func floatFromAny(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	default:
		return 0, false
	}
}

func stringFromAny(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
