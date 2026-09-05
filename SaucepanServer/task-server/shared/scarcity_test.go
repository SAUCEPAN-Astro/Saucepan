package shared

import "testing"

func TestEffectivePenalty_decaysWithIdleTime(t *testing.T) {
	p := EffectivePenalty(0, IdleSaturationMinutes)
	if p != 0 {
		t.Fatalf("expected 0 penalty after saturation idle, got %v", p)
	}
	p = EffectivePenalty(0, IdleSaturationMinutes*2)
	if p != 0 {
		t.Fatalf("expected 0 penalty beyond saturation, got %v", p)
	}
}

func TestEffectivePenalty_highWhenScarceAndFresh(t *testing.T) {
	fresh := EffectivePenalty(0, 0)
	busy := EffectivePenalty(5, 0)
	if fresh <= busy {
		t.Fatalf("expected fresh scarce penalty > busy substitute penalty: %v vs %v", fresh, busy)
	}
}
