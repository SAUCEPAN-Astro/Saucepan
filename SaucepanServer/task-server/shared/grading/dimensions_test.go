package grading_test

import (
	"testing"

	"github.com/saucepan/hotpath/shared/grading"
)

func TestHeadlineScore(t *testing.T) {
	dims := map[string]any{
		"image_quality": map[string]any{"score": 0.8},
		"task_fidelity": map[string]any{"score": 0.6},
		"timeliness":    map[string]any{"score": 1.0},
	}
	got := grading.HeadlineScore(dims)
	// 0.4*0.8 + 0.35*0.6 + 0.25*1.0 = 0.32+0.21+0.25 = 0.78 → 78
	if got != 78 {
		t.Fatalf("headline=%d want 78", got)
	}
}

func TestScoreTimeliness(t *testing.T) {
	ctx := map[string]any{
		"assignment_sent_at":  "2026-06-05T12:00:00Z",
		"upload_completed_at": "2026-06-05T12:10:00Z",
	}
	out := grading.ScoreTimeliness(ctx)
	score, ok := out["score"].(float64)
	if !ok || score < 0.9 {
		t.Fatalf("timeliness score too low: %v", out)
	}
}

func TestScoreTaskFidelityUsesExactFilterTokens(t *testing.T) {
	out := grading.ScoreTaskFidelity(grading.SPHeaders{Filter: "IR"}, map[string]any{
		"filter_requested": "R",
	})
	match, ok := out["filter_match"].(*bool)
	if !ok || match == nil || *match {
		t.Fatalf("filter_match=%v, want false for R vs IR", out["filter_match"])
	}
}
