package grading

import "testing"

// Lightweight smoke tests. Numeric parity is enforced by parity_test.go
// against SaucepanServer/contracts/grading/*.json.

func TestComputeFramePointsPositive(t *testing.T) {
	grade := map[string]any{
		"dimensions": map[string]any{
			"image_quality": map[string]any{"score": 0.8},
			"timeliness":    map[string]any{"score": 0.6},
		},
		"sp_exptime": 30.0,
	}
	stats := map[string]any{"total_exposure_seconds": 3600.0}
	b := ComputeFramePoints(grade, stats, 1.0)
	if b.PointsEarned <= 0 {
		t.Fatalf("expected positive points, got %v", b.PointsEarned)
	}
}

func TestBuildReputationPartialSource(t *testing.T) {
	dims := map[string]any{
		"image_quality": map[string]any{"score": 0.7},
	}
	partial := BuildReputationPartial(nil, 75, dims, 12.5, 20.0)
	if partial["source"] != "grade_ingest" {
		t.Fatalf("source = %v", partial["source"])
	}
	if _, ok := partial["last_ingested_at"]; !ok {
		t.Fatal("missing last_ingested_at")
	}
}
