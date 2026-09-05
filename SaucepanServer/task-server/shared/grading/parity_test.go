package grading

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
)

func vectorsDir(t *testing.T) string {
	t.Helper()
	if v := os.Getenv("SP_GRADING_VECTORS"); v != "" {
		return v
	}
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// shared/grading → SaucepanServer/contracts/grading
	dir := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "contracts", "grading"))
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		t.Fatalf("shared grading vectors missing at %s (set SP_GRADING_VECTORS): %v", dir, err)
	}
	return dir
}

func loadJSON(t *testing.T, name string, dest any) {
	t.Helper()
	path := filepath.Join(vectorsDir(t), name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(raw, dest); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
}

func TestParityVectorsPresent(t *testing.T) {
	dir := vectorsDir(t)
	for _, name := range []string{
		"constants.json",
		"points_vectors.json",
		"reputation_vectors.json",
		"stack_vectors.json",
		"headline_vectors.json",
		"dimensions_vectors.json",
		"grade_ingest_min.json",
	} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
}

func TestConstantsMatchSnapshot(t *testing.T) {
	var doc struct {
		Constants map[string]any `json:"constants"`
	}
	loadJSON(t, "constants.json", &doc)
	c := doc.Constants
	if len(c) == 0 {
		t.Fatal("constants collection is empty")
	}

	assertFloatConst(t, "BASE_POINTS", BasePoints, c)
	assertFloatConst(t, "EXPTIME_CAP_SECONDS", ExptimeCapSeconds, c)
	assertFloatConst(t, "TENURE_LOG_SCALE", TenureLogScale, c)
	assertFloatConst(t, "STACK_ELIGIBLE_MIN_QUALITY", StackEligibleMinQuality, c)
	assertFloatConst(t, "RELIABILITY_EMA_ALPHA", ReliabilityEMAAlpha, c)
	assertFloatConst(t, "HEADLINE_EMA_ALPHA", HeadlineEMAAlpha, c)
	assertFloatConst(t, "SNR_FULL_CREDIT", SNRFullCredit, c)
	assertFloatConst(t, "SATURATION_PENALTY_FRACTION", SaturationPenaltyFraction, c)
	assertFloatConst(t, "NEUTRAL_FWHM_SCORE", NeutralFWHMScore, c)
	assertFloatConst(t, "FILTER_ABSENT_SCORE", FilterAbsentScore, c)
	assertFloatConst(t, "CALIBRATION_BONUS", CalibrationBonus, c)
	assertFloatConst(t, "CAPTURE_LATENCY_FULL_SEC", CaptureLatencyFullSec, c)
	assertFloatConst(t, "CAPTURE_LATENCY_ZERO_SEC", CaptureLatencyZeroSec, c)
	assertFloatConst(t, "UPLOAD_DURATION_FULL_SEC", UploadDurationFullSec, c)
	assertFloatConst(t, "UPLOAD_DURATION_ZERO_SEC", UploadDurationZeroSec, c)
	assertFloatConst(t, "TIMELINESS_CAPTURE_WEIGHT", TimelinessCaptureWeight, c)
	assertFloatConst(t, "TIMELINESS_UPLOAD_WEIGHT", TimelinessUploadWeight, c)
	assertFloatConst(t, "MISSING_TIMELINESS_SCORE", MissingTimelinessScore, c)

	assertWeightMap(t, "CHEAP_DIMENSION_WEIGHTS", CheapDimensionWeights, c)
	assertWeightMap(t, "IMAGE_QUALITY_WEIGHTS", ImageQualityWeights, c)
}

func TestPointsVectors(t *testing.T) {
	var doc struct {
		Cases []struct {
			Name               string         `json:"name"`
			Grade              map[string]any `json:"grade"`
			TelescopeStats     map[string]any `json:"telescope_stats"`
			CampaignMultiplier float64        `json:"campaign_multiplier"`
			Expected           map[string]any `json:"expected"`
		} `json:"cases"`
	}
	loadJSON(t, "points_vectors.json", &doc)
	if len(doc.Cases) == 0 {
		t.Fatal("no points cases")
	}
	for _, c := range doc.Cases {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			got := ComputeFramePoints(c.Grade, c.TelescopeStats, c.CampaignMultiplier)
			assertBreakdown(t, got, c.Expected)
		})
	}
}

func TestStackVectors(t *testing.T) {
	var doc struct {
		Cases []struct {
			Name       string         `json:"name"`
			Dimensions map[string]any `json:"dimensions"`
			Expected   bool           `json:"expected"`
		} `json:"cases"`
	}
	loadJSON(t, "stack_vectors.json", &doc)
	if len(doc.Cases) == 0 {
		t.Fatal("stack cases collection is empty")
	}
	for _, c := range doc.Cases {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			if IsStackEligible(c.Dimensions) != c.Expected {
				t.Fatalf("IsStackEligible = %v, want %v", IsStackEligible(c.Dimensions), c.Expected)
			}
		})
	}
}

func TestHeadlineVectors(t *testing.T) {
	var doc struct {
		Cases []struct {
			Name       string         `json:"name"`
			Dimensions map[string]any `json:"dimensions"`
			Expected   int            `json:"expected"`
		} `json:"cases"`
	}
	loadJSON(t, "headline_vectors.json", &doc)
	if len(doc.Cases) == 0 {
		t.Fatal("headline cases collection is empty")
	}
	for _, c := range doc.Cases {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			if got := HeadlineScore(c.Dimensions); got != c.Expected {
				t.Fatalf("HeadlineScore = %d, want %d", got, c.Expected)
			}
		})
	}
}

func TestEMAVectors(t *testing.T) {
	var doc struct {
		EMACases []struct {
			Name     string   `json:"name"`
			Previous *float64 `json:"previous"`
			Sample   float64  `json:"sample"`
			Alpha    float64  `json:"alpha"`
			Expected float64  `json:"expected"`
		} `json:"ema_cases"`
	}
	loadJSON(t, "reputation_vectors.json", &doc)
	if len(doc.EMACases) == 0 {
		t.Fatal("EMA cases collection is empty")
	}
	for _, c := range doc.EMACases {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			got := EMAUpdate(c.Previous, c.Sample, c.Alpha)
			if got != c.Expected {
				t.Fatalf("EMAUpdate = %v, want %v", got, c.Expected)
			}
		})
	}
}

func TestReputationVectors(t *testing.T) {
	var doc struct {
		ReputationCases []struct {
			Name         string         `json:"name"`
			Existing     map[string]any `json:"existing"`
			Headline     int            `json:"headline"`
			Dimensions   map[string]any `json:"dimensions"`
			PointsEarned float64        `json:"points_earned"`
			OAExptime    float64        `json:"sp_exptime"`
			Expected     map[string]any `json:"expected"`
		} `json:"reputation_cases"`
	}
	loadJSON(t, "reputation_vectors.json", &doc)
	if len(doc.ReputationCases) == 0 {
		t.Fatal("reputation cases collection is empty")
	}
	for _, c := range doc.ReputationCases {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			got := BuildReputationPartial(c.Existing, c.Headline, c.Dimensions, c.PointsEarned, c.OAExptime)
			for key, want := range c.Expected {
				gv, ok := got[key]
				if !ok {
					t.Fatalf("missing key %s", key)
				}
				if !jsonEqual(gv, want) {
					t.Fatalf("%s: got %#v want %#v", key, gv, want)
				}
			}
		})
	}
}

func TestGradeIngestMinShape(t *testing.T) {
	// No grade-ingest validator exists in this package; this intentionally
	// checks only the documented fixture shape and must not be presented as
	// validator coverage.
	var doc struct {
		Required      []string       `json:"required"`
		Example       map[string]any `json:"example"`
		DimensionKeys []string       `json:"dimension_keys"`
	}
	loadJSON(t, "grade_ingest_min.json", &doc)
	if len(doc.Required) == 0 || len(doc.DimensionKeys) == 0 || len(doc.Example) == 0 {
		t.Fatal("grade-ingest fixture collections are empty")
	}
	for _, key := range doc.Required {
		if _, ok := doc.Example[key]; !ok {
			t.Fatalf("example missing required %s", key)
		}
	}
	dims, _ := doc.Example["dimensions"].(map[string]any)
	for _, key := range doc.DimensionKeys {
		dim, _ := dims[key].(map[string]any)
		if _, ok := dim["score"]; !ok {
			t.Fatalf("dimensions.%s missing score", key)
		}
	}
}

func TestDimensionVectors(t *testing.T) {
	var doc struct {
		ImageQualityCases []struct {
			Name         string         `json:"name"`
			Metrics      map[string]any `json:"metrics"`
			Headers      map[string]any `json:"headers"`
			PredictedPSF *float64       `json:"predicted_psf_arcsec"`
			Expected     map[string]any `json:"expected"`
		} `json:"image_quality_cases"`
		TaskFidelityCases []struct {
			Name        string         `json:"name"`
			Headers     map[string]any `json:"headers"`
			TaskContext map[string]any `json:"task_context"`
			Expected    map[string]any `json:"expected"`
		} `json:"task_fidelity_cases"`
		TimelinessCases []struct {
			Name        string         `json:"name"`
			TaskContext map[string]any `json:"task_context"`
			Expected    map[string]any `json:"expected"`
		} `json:"timeliness_cases"`
	}
	loadJSON(t, "dimensions_vectors.json", &doc)
	if len(doc.ImageQualityCases) == 0 {
		t.Fatal("image quality cases collection is empty")
	}
	if len(doc.TaskFidelityCases) == 0 {
		t.Fatal("task fidelity cases collection is empty")
	}
	if len(doc.TimelinessCases) == 0 {
		t.Fatal("timeliness cases collection is empty")
	}

	for _, c := range doc.ImageQualityCases {
		c := c
		t.Run("image_quality/"+c.Name, func(t *testing.T) {
			got := ScoreImageQuality(qualityMetricsFromVector(c.Metrics), headersFromVector(c.Headers), c.PredictedPSF)
			assertJSONMap(t, got, c.Expected)
		})
	}
	for _, c := range doc.TaskFidelityCases {
		c := c
		t.Run("task_fidelity/"+c.Name, func(t *testing.T) {
			got := ScoreTaskFidelity(headersFromVector(c.Headers), c.TaskContext)
			assertJSONMap(t, got, c.Expected)
		})
	}
	for _, c := range doc.TimelinessCases {
		c := c
		t.Run("timeliness/"+c.Name, func(t *testing.T) {
			got := ScoreTimeliness(c.TaskContext)
			assertJSONMap(t, got, c.Expected)
		})
	}
}

func qualityMetricsFromVector(raw map[string]any) QualityMetrics {
	shape := [2]int{}
	if values, ok := raw["shape"].([]any); ok && len(values) == 2 {
		shape[0] = int(values[0].(float64))
		shape[1] = int(values[1].(float64))
	}
	return QualityMetrics{
		SNR:             floatFromMap(raw, "snr"),
		NoiseADU:        floatFromMap(raw, "noise_adu"),
		StarPixels:      intFromMap(raw, "star_pixels"),
		SaturatedPixels: intFromMap(raw, "saturated_pixels"),
		Shape:           shape,
	}
}

func headersFromVector(raw map[string]any) SPHeaders {
	var out SPHeaders
	if v, ok := raw["sp_exptime"].(float64); ok {
		out.Exptime = &v
	}
	if v, ok := raw["sp_fwhm"].(float64); ok {
		out.FWHM = &v
	}
	if v, ok := raw["sp_qual"].(float64); ok {
		out.Qual = &v
	}
	out.Filter = stringFromAny(raw["sp_filter"])
	out.Calstat = stringFromAny(raw["sp_calstat"])
	return out
}

func assertJSONMap(t *testing.T, got, expected map[string]any) {
	t.Helper()
	if len(got) != len(expected) {
		t.Fatalf("key set mismatch: got %v want %v", mapKeys(got), mapKeys(expected))
	}
	for key, want := range expected {
		value, ok := got[key]
		if !ok || !jsonEqual(value, want) {
			t.Fatalf("%s: got %#v want %#v", key, value, want)
		}
	}
}

func mapKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func assertBreakdown(t *testing.T, got PointsBreakdown, expected map[string]any) {
	t.Helper()
	checks := map[string]float64{
		"base_points":         got.BasePoints,
		"quality_multiplier":  got.QualityMultiplier,
		"exptime_factor":      got.ExptimeFactor,
		"timeliness_factor":   got.TimelinessFactor,
		"tenure_multiplier":   got.TenureMultiplier,
		"campaign_multiplier": got.CampaignMultiplier,
		"sp_exptime":          got.OAExptime,
		"points_earned":       got.PointsEarned,
	}
	for key, g := range checks {
		want, ok := expected[key]
		if !ok {
			t.Fatalf("expected missing key %s", key)
		}
		wf, ok := want.(float64)
		if !ok {
			t.Fatalf("%s: expected float64, got %T", key, want)
		}
		if g != wf {
			t.Fatalf("%s: got %v want %v", key, g, wf)
		}
	}
}

func assertFloatConst(t *testing.T, name string, got float64, c map[string]any) {
	t.Helper()
	want, ok := c[name].(float64)
	if !ok {
		t.Fatalf("%s: snapshot type %T", name, c[name])
	}
	if got != want {
		t.Fatalf("%s: got %v want %v", name, got, want)
	}
}

func assertWeightMap(t *testing.T, name string, got map[string]float64, c map[string]any) {
	t.Helper()
	raw, ok := c[name].(map[string]any)
	if !ok {
		t.Fatalf("%s: snapshot type %T", name, c[name])
	}
	if len(got) != len(raw) {
		t.Fatalf("%s: len got %d want %d", name, len(got), len(raw))
	}
	for k, v := range got {
		want, ok := raw[k].(float64)
		if !ok || v != want {
			t.Fatalf("%s[%s]: got %v want %v", name, k, v, raw[k])
		}
	}
}

func jsonEqual(a, b any) bool {
	ab, err1 := json.Marshal(a)
	bb, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		return false
	}
	var ai, bi any
	if err := json.Unmarshal(ab, &ai); err != nil {
		return false
	}
	if err := json.Unmarshal(bb, &bi); err != nil {
		return false
	}
	return jsonDeepEqual(ai, bi)
}

func jsonDeepEqual(a, b any) bool {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, v := range av {
			if !jsonDeepEqual(v, bv[k]) {
				return false
			}
		}
		return true
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !jsonDeepEqual(av[i], bv[i]) {
				return false
			}
		}
		return true
	case float64:
		bv, ok := b.(float64)
		return ok && av == bv
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	case nil:
		return b == nil
	default:
		return false
	}
}
