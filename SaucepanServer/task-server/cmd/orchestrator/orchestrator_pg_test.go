package main

import (
	"testing"

	"github.com/saucepan/hotpath/shared"
)

func TestApplyCoverageFromPack(t *testing.T) {
	// nil payload must not panic.
	applyCoverageFromPack(nil, []byte(`{"coverage":{"enabled":true}}`))

	// Empty packJSON is a no-op.
	p := shared.NotifyPayload{}
	applyCoverageFromPack(&p, nil)
	if p.CoverageEnabled {
		t.Fatal("empty packJSON should leave CoverageEnabled false")
	}

	// Malformed JSON is a no-op (json.Unmarshal error swallowed).
	p2 := shared.NotifyPayload{}
	applyCoverageFromPack(&p2, []byte(`not json`))
	if p2.CoverageEnabled {
		t.Fatal("malformed packJSON should leave CoverageEnabled false")
	}

	// Coverage explicitly disabled (or absent) is a no-op.
	p3 := shared.NotifyPayload{}
	applyCoverageFromPack(&p3, []byte(`{"coverage":{"enabled":false}}`))
	if p3.CoverageEnabled {
		t.Fatal("coverage.enabled=false should leave CoverageEnabled false")
	}
	p3b := shared.NotifyPayload{}
	applyCoverageFromPack(&p3b, []byte(`{}`))
	if p3b.CoverageEnabled {
		t.Fatal("pack with no coverage key should leave CoverageEnabled false")
	}

	// Enabled + soft mode (default) — CoverageHardMode false.
	p4 := shared.NotifyPayload{}
	applyCoverageFromPack(&p4, []byte(`{"coverage":{"enabled":true,"mode":"soft"}}`))
	if !p4.CoverageEnabled || p4.CoverageHardMode {
		t.Fatalf("soft mode: CoverageEnabled=%v CoverageHardMode=%v, want true/false", p4.CoverageEnabled, p4.CoverageHardMode)
	}

	// Hard mode is case-insensitive and trims whitespace.
	p5 := shared.NotifyPayload{}
	applyCoverageFromPack(&p5, []byte(`{"coverage":{"enabled":true,"mode":" HARD "}}`))
	if !p5.CoverageEnabled || !p5.CoverageHardMode {
		t.Fatalf("hard mode: CoverageEnabled=%v CoverageHardMode=%v, want true/true", p5.CoverageEnabled, p5.CoverageHardMode)
	}

	// Coverage plan primary/redundant carried over as independent copies.
	p6 := shared.NotifyPayload{}
	applyCoverageFromPack(&p6, []byte(`{"coverage":{"enabled":true},"coverage_plan":{"primary":["n1","n2"],"redundant":["n3"]}}`))
	if len(p6.CoveragePrimary) != 2 || p6.CoveragePrimary[0] != "n1" {
		t.Fatalf("CoveragePrimary = %v, want [n1 n2]", p6.CoveragePrimary)
	}
	if len(p6.CoverageRedundant) != 1 || p6.CoverageRedundant[0] != "n3" {
		t.Fatalf("CoverageRedundant = %v, want [n3]", p6.CoverageRedundant)
	}

	// Enabled with no coverage_plan key leaves Primary/Redundant nil, not panicking.
	p7 := shared.NotifyPayload{}
	applyCoverageFromPack(&p7, []byte(`{"coverage":{"enabled":true}}`))
	if p7.CoveragePrimary != nil || p7.CoverageRedundant != nil {
		t.Fatalf("expected nil coverage plan slices, got primary=%v redundant=%v", p7.CoveragePrimary, p7.CoverageRedundant)
	}
}

func TestApplyPierCodeFromPack(t *testing.T) {
	// nil payload / empty JSON / malformed JSON must not panic and must no-op.
	applyPierCodeFromPack(nil, []byte(`{"pier_code":{"enabled":true}}`))
	p := shared.NotifyPayload{}
	applyPierCodeFromPack(&p, nil)
	applyPierCodeFromPack(&p, []byte(`not json`))
	if p.PierCodeGrants != nil {
		t.Fatalf("no pack → PierCodeGrants should stay nil, got %v", p.PierCodeGrants)
	}

	// pier_code absent → nil (pier runs nothing).
	p1 := shared.NotifyPayload{}
	applyPierCodeFromPack(&p1, []byte(`{"targets":[]}`))
	if p1.PierCodeGrants != nil {
		t.Fatalf("pier_code absent → nil, got %v", p1.PierCodeGrants)
	}

	// enabled, no actions map → the read+board default.
	p2 := shared.NotifyPayload{}
	applyPierCodeFromPack(&p2, []byte(`{"pier_code":{"enabled":true}}`))
	if !p2.PierCodeGrants["read_frame"] || !p2.PierCodeGrants["board_post"] || !p2.PierCodeGrants["board_read"] {
		t.Fatalf("default grants missing read/board: %v", p2.PierCodeGrants)
	}
	if p2.PierCodeGrants["next_capture"] {
		t.Fatalf("default grants must not include next_capture: %v", p2.PierCodeGrants)
	}

	// explicit actions map is carried through, unknown keys dropped.
	p3 := shared.NotifyPayload{}
	applyPierCodeFromPack(&p3, []byte(`{"pier_code":{"enabled":true,"actions":{"read_frame":true,"next_capture":true,"bogus":true}}}`))
	if !p3.PierCodeGrants["read_frame"] || !p3.PierCodeGrants["next_capture"] {
		t.Fatalf("explicit grants not carried: %v", p3.PierCodeGrants)
	}
	if _, ok := p3.PierCodeGrants["bogus"]; ok {
		t.Fatalf("unknown action should be dropped: %v", p3.PierCodeGrants)
	}

	// enabled:false → nil.
	p4 := shared.NotifyPayload{}
	applyPierCodeFromPack(&p4, []byte(`{"pier_code":{"enabled":false,"actions":{"read_frame":true}}}`))
	if p4.PierCodeGrants != nil {
		t.Fatalf("enabled:false → nil, got %v", p4.PierCodeGrants)
	}

	// pier_code enabled with no artifact → grants set, PierCode nil.
	p5 := shared.NotifyPayload{}
	applyPierCodeFromPack(&p5, []byte(`{"pier_code":{"enabled":true}}`))
	if p5.PierCode != nil {
		t.Fatalf("no artifact → PierCode nil, got %+v", p5.PierCode)
	}

	// pier_code with an artifact → PierCode hydrated with hash + url.
	hash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	p6 := shared.NotifyPayload{}
	applyPierCodeFromPack(&p6, []byte(`{"pier_code":{"enabled":true,"artifact":{"sha256":"`+hash+`","url":"https://example.test/a.wasm"}}}`))
	if p6.PierCode == nil || p6.PierCode.SHA256 != hash || p6.PierCode.URL != "https://example.test/a.wasm" {
		t.Fatalf("artifact not hydrated: %+v", p6.PierCode)
	}

	// enabled:false with an artifact → PierCode nil.
	p7 := shared.NotifyPayload{}
	applyPierCodeFromPack(&p7, []byte(`{"pier_code":{"enabled":false,"artifact":{"sha256":"`+hash+`","url":"https://example.test/a.wasm"}}}`))
	if p7.PierCode != nil {
		t.Fatalf("enabled:false → PierCode nil, got %+v", p7.PierCode)
	}
}
