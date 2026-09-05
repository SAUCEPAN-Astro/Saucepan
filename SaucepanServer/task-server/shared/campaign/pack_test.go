package campaign

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/saucepan/hotpath/shared/wire"
)

func TestValidateProduct_Modes(t *testing.T) {
	if err := ValidateProduct(nil); err != nil {
		t.Fatalf("nil product ok: %v", err)
	}
	if err := ValidateProduct(&ProductIntent{Mode: "per_frame"}); err != nil {
		t.Fatalf("per_frame: %v", err)
	}
	if err := ValidateProduct(&ProductIntent{Mode: "stack"}); err != nil {
		t.Fatalf("stack: %v", err)
	}
	if err := ValidateProduct(&ProductIntent{Mode: "time_bin", TimeBinFrames: 10}); err != nil {
		t.Fatalf("time_bin: %v", err)
	}
	if err := ValidateProduct(&ProductIntent{Mode: "time_bin"}); err == nil {
		t.Fatal("expected time_bin without frames to fail")
	}
	if err := ValidateProduct(&ProductIntent{Mode: "per_frame", TimeBinFrames: 5}); err == nil {
		t.Fatal("expected per_frame+time_bin_frames to fail")
	}
	if NormalizedProductMode(nil) != "per_frame" {
		t.Fatal("default mode must be per_frame")
	}
	if WantsStack(nil) {
		t.Fatal("default must not want stack")
	}
	if !WantsStack(&ProductIntent{Mode: "stack"}) {
		t.Fatal("stack mode should want stack")
	}
}

func TestCanonicalPackJSON_ProductPerFrame(t *testing.T) {
	raw := []byte(`{
		"name": "exo",
		"test_only": true,
		"product": {"mode": "per_frame"},
		"targets": [{"ra": 1, "dec": 2, "filters": ["V"], "exposure_sec": 30, "frame_count": 10}]
	}`)
	p, canonical, err := CanonicalPackJSON(raw)
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	if p.Product == nil || p.Product.Mode != "per_frame" {
		t.Fatalf("product=%+v", p.Product)
	}
	if err := ValidatePack(p); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if WantsStack(p.Product) {
		t.Fatal("time-domain pack must not route to stack")
	}
	var obj map[string]any
	if err := json.Unmarshal(canonical, &obj); err != nil {
		t.Fatal(err)
	}
	prod, ok := obj["product"].(map[string]any)
	if !ok || prod["mode"] != "per_frame" {
		t.Fatalf("canonical product=%v", obj["product"])
	}
}

func TestExpandPack_GenericFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/generic_campaign.json")
	if err != nil {
		t.Fatalf("read demo pack: %v", err)
	}
	p, err := ParsePack(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	specs, err := ExpandPack(p)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("got %d tasks, want 1", len(specs))
	}
	s := specs[0]
	if s.NormalizedIntegrationBudgetS != 60 {
		t.Fatalf("budget=%v want 60 (3×20)", s.NormalizedIntegrationBudgetS)
	}
	if s.IntegrationTime != 20 {
		t.Fatalf("integration_time=%v want 20", s.IntegrationTime)
	}
	if len(s.RequiredFilters) != 1 || s.RequiredFilters[0] != "R" {
		t.Fatalf("filters=%v", s.RequiredFilters)
	}
	if !s.AllowEmulator {
		t.Fatal("test_only campaign should allow emulator")
	}
}

func TestCanonicalPackJSON_RejectsUnknownTopLevel(t *testing.T) {
	raw := []byte(`{
		"name": "bad",
		"transform_table_ref": "file:///etc/passwd",
		"targets": [{"ra": 1, "dec": 2, "filters": ["R"], "exposure_sec": 1, "frame_count": 1}]
	}`)
	_, _, err := CanonicalPackJSON(raw)
	if err == nil {
		t.Fatal("expected rejection of transform_table_ref")
	}
	if !strings.Contains(err.Error(), "transform_table_ref") {
		t.Fatalf("error=%v want transform_table_ref mention", err)
	}
}

func TestCanonicalPackJSON_RejectsUnsafeRefFieldAndVeto(t *testing.T) {
	raw := []byte(`{
		"name": "bad",
		"veto_policy": {"mode": "deny"},
		"targets": [{"ra": 1, "dec": 2, "filters": ["R"], "exposure_sec": 1, "frame_count": 1}]
	}`)
	_, _, err := CanonicalPackJSON(raw)
	if err == nil || !strings.Contains(err.Error(), "veto_policy") {
		t.Fatalf("error=%v want veto_policy rejection", err)
	}
}

func TestCanonicalPackJSON_StoresOnlyKnownFields(t *testing.T) {
	raw := []byte(`{
		"name": "ok",
		"test_only": true,
		"description": "hello",
		"targets": [{"ra": 1.5, "dec": -2, "filters": ["R"], "exposure_sec": 10, "frame_count": 2}]
	}`)
	p, canonical, err := CanonicalPackJSON(raw)
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	if p.Name != "ok" || !p.TestOnly {
		t.Fatalf("pack=%+v", p)
	}
	var obj map[string]any
	if err := json.Unmarshal(canonical, &obj); err != nil {
		t.Fatalf("unmarshal canonical: %v", err)
	}
	for _, banned := range []string{"transform_table_ref", "veto_policy", "deliver_mode"} {
		if _, ok := obj[banned]; ok {
			t.Fatalf("canonical still has %q: %s", banned, canonical)
		}
	}
	if obj["name"] != "ok" {
		t.Fatalf("canonical=%s", canonical)
	}
}

func TestValidateStoredPackJSON_AllowsCoveragePlan(t *testing.T) {
	raw := []byte(`{
		"name": "cov",
		"targets": [{"ra": 0, "dec": 0, "filters": ["R"], "exposure_sec": 1, "frame_count": 1}],
		"coverage_plan": {"primary": ["a"], "redundant": []}
	}`)
	if err := ValidateStoredPackJSON(raw); err != nil {
		t.Fatalf("server coverage_plan should be allowed: %v", err)
	}
	if err := ValidateStoredPackJSON([]byte(`{"name":"x","transform_table_ref":"http://169.254.169.254/","targets":[]}`)); err == nil {
		t.Fatal("expected reject of transform_table_ref in stored pack")
	}
}

func TestCanonicalPackJSON_SeasonAndCoverageHard(t *testing.T) {
	raw := []byte(`{
		"name": "betel",
		"test_only": true,
		"season": {"kind": "continuous", "urgency": "normal", "target_duty_cycle": 0.7},
		"coverage": {
			"enabled": true,
			"mode": "hard",
			"n_main": 2,
			"min_sites": 2,
			"min_longitude_span_deg": 90,
			"preferred_sites": ["pier-a"]
		},
		"targets": [{
			"ra": 88.8, "dec": 7.4,
			"filters": ["V"],
			"exposure_sec": 1,
			"frame_count": 10,
			"saturation": {"strategy": "short", "max_exposure_sec": 2}
		}]
	}`)
	p, _, err := CanonicalPackJSON(raw)
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	if p.Season == nil || p.Season.Kind != "continuous" {
		t.Fatalf("season=%+v", p.Season)
	}
	if p.Coverage == nil || p.Coverage.Mode != "hard" || p.Coverage.MinSites != 2 {
		t.Fatalf("coverage=%+v", p.Coverage)
	}
	if err := ValidatePack(p); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestValidatePack_SaturationExposure(t *testing.T) {
	maxExp := 1.0
	p := &Pack{
		Name: "sat",
		Targets: []PackTarget{{
			RA: 1, Dec: 2, Filters: []string{"V"},
			ExposureSec: 5, FrameCount: 1,
			Saturation: &SaturationHint{MaxExposureSec: &maxExp, Strategy: "short"},
		}},
	}
	if err := ValidatePack(p); err == nil {
		t.Fatal("expected exposure vs max_exposure_sec failure")
	}
}

func TestExpandPack_MultiFilter(t *testing.T) {
	p := &Pack{
		Name: "multi",
		Targets: []PackTarget{
			{
				RA: 1, Dec: 2,
				Filters:     []string{"R", "G"},
				ExposureSec: 10,
				FrameCount:  2,
			},
		},
	}
	specs, err := ExpandPack(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 2 {
		t.Fatalf("got %d tasks, want 2", len(specs))
	}
}

func TestValidateHookPublish_ComputeRequiresApproval(t *testing.T) {
	p := &Pack{
		Name:          "hooked",
		HookPlacement: "compute",
		HookImageRef:  "ghcr.io/example/hook@sha256:abc",
		Targets: []PackTarget{
			{RA: 0, Dec: 0, Filters: []string{"R"}, ExposureSec: 1, FrameCount: 1},
		},
	}
	if err := ValidateHookPublish(p, false); err != ErrHookNotApproved {
		t.Fatalf("got %v want ErrHookNotApproved", err)
	}
	if err := ValidateHookPublish(p, true); err != nil {
		t.Fatalf("approved: %v", err)
	}
}

func TestValidateHookPublish_EdgeNoOperatorFlag(t *testing.T) {
	p := &Pack{
		Name:          "edge-hook",
		HookPlacement: "edge",
		HookImageRef:  "ghcr.io/example/edge@sha256:def",
		Targets: []PackTarget{
			{RA: 0, Dec: 0, Filters: []string{"R"}, ExposureSec: 1, FrameCount: 1},
		},
	}
	if err := ValidateHookPublish(p, false); err != nil {
		t.Fatalf("edge hook should publish without hook_approved: %v", err)
	}
}

func TestEffectivePierCodeGrants(t *testing.T) {
	// No pier_code block → nil (pier runs nothing).
	if g := EffectivePierCodeGrants(&Pack{Name: "x"}); g != nil {
		t.Fatalf("no pier_code → nil, got %v", g)
	}
	// enabled:false → nil.
	if g := EffectivePierCodeGrants(&Pack{PierCode: &PierCodeIntent{Enabled: false}}); g != nil {
		t.Fatalf("enabled:false → nil, got %v", g)
	}
	// enabled, no actions → read+board default.
	g := EffectivePierCodeGrants(&Pack{PierCode: &PierCodeIntent{Enabled: true}})
	if !g["read_frame"] || !g["board_post"] || !g["board_read"] || g["inbox_alert"] {
		t.Fatalf("default grants wrong: %v", g)
	}
	// explicit actions → filtered to known keys.
	g = EffectivePierCodeGrants(&Pack{PierCode: &PierCodeIntent{
		Enabled: true,
		Actions: map[string]bool{"next_capture": true, "board_post": false, "nope": true},
	}})
	if !g["next_capture"] || g["board_post"] {
		t.Fatalf("explicit grants wrong: %v", g)
	}
	if _, ok := g["nope"]; ok {
		t.Fatalf("unknown action not dropped: %v", g)
	}
}

func TestEffectivePierCodeArtifact(t *testing.T) {
	ref := &wire.PierCodeRef{
		SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		URL:    "https://example.test/a.wasm",
	}
	// No pier_code → nil.
	if a := EffectivePierCodeArtifact(&Pack{Name: "x"}); a != nil {
		t.Fatalf("no pier_code → nil, got %v", a)
	}
	// Enabled but no artifact → nil (grants-only campaign).
	if a := EffectivePierCodeArtifact(&Pack{PierCode: &PierCodeIntent{Enabled: true}}); a != nil {
		t.Fatalf("no artifact → nil, got %v", a)
	}
	// enabled:false with an artifact → still nil.
	if a := EffectivePierCodeArtifact(&Pack{PierCode: &PierCodeIntent{Enabled: false, Artifact: ref}}); a != nil {
		t.Fatalf("enabled:false → nil, got %v", a)
	}
	// Enabled + artifact → a copy of the ref.
	a := EffectivePierCodeArtifact(&Pack{PierCode: &PierCodeIntent{Enabled: true, Artifact: ref}})
	if a == nil || a.SHA256 != ref.SHA256 || a.URL != ref.URL {
		t.Fatalf("artifact not returned: %+v", a)
	}
	if a == ref {
		t.Fatal("EffectivePierCodeArtifact must return a copy, not the pack's pointer")
	}
	// Malformed hash → nil (defensive; ValidatePack rejects this earlier).
	bad := EffectivePierCodeArtifact(&Pack{PierCode: &PierCodeIntent{
		Enabled:  true,
		Artifact: &wire.PierCodeRef{SHA256: "short", URL: "https://x/y"},
	}})
	if bad != nil {
		t.Fatalf("malformed artifact → nil, got %v", bad)
	}
}

func TestValidatePackRejectsBadPierCodeArtifact(t *testing.T) {
	base := func() *Pack {
		return &Pack{
			Name:    "c",
			Targets: []PackTarget{{Filters: []string{"R"}, ExposureSec: 30, FrameCount: 1}},
			PierCode: &PierCodeIntent{
				Enabled: true,
				Artifact: &wire.PierCodeRef{
					SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
					URL:    "https://example.test/a.wasm",
				},
			},
		}
	}
	if err := ValidatePack(base()); err != nil {
		t.Fatalf("well-formed artifact should pass: %v", err)
	}

	noURL := base()
	noURL.PierCode.Artifact.URL = ""
	if err := ValidatePack(noURL); err == nil || !strings.Contains(err.Error(), "url is required") {
		t.Fatalf("want url-required error, got %v", err)
	}

	badHash := base()
	badHash.PierCode.Artifact.SHA256 = "nope"
	if err := ValidatePack(badHash); err == nil || !strings.Contains(err.Error(), "pier_code.artifact") {
		t.Fatalf("want artifact hash error, got %v", err)
	}
}

func TestValidatePackRejectsUnknownPierCodeAction(t *testing.T) {
	p := &Pack{
		Name:     "c",
		Targets:  []PackTarget{{Filters: []string{"R"}, ExposureSec: 30, FrameCount: 1}},
		PierCode: &PierCodeIntent{Enabled: true, Actions: map[string]bool{"rm_rf": true}},
	}
	if err := ValidatePack(p); err == nil || !strings.Contains(err.Error(), "unknown action") {
		t.Fatalf("want unknown-action error, got %v", err)
	}
	// A known action passes.
	p.PierCode.Actions = map[string]bool{"next_capture": true}
	if err := ValidatePack(p); err != nil {
		t.Fatalf("known action should pass: %v", err)
	}
}

func TestCanonicalPackJSON_PierCodeRoundTrips(t *testing.T) {
	hash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	raw := []byte(`{"name":"c","targets":[{"ra":1,"dec":2,"filters":["R"],"exposure_sec":30,"frame_count":1}],"pier_code":{"enabled":true,"actions":{"read_frame":true,"next_capture":true},"artifact":{"sha256":"` + hash + `","url":"https://example.test/a.wasm"}}}`)
	pack, canonical, err := CanonicalPackJSON(raw)
	if err != nil {
		t.Fatalf("CanonicalPackJSON: %v", err)
	}
	if pack.PierCode == nil || !pack.PierCode.Enabled || !pack.PierCode.Actions["next_capture"] {
		t.Fatalf("pier_code not parsed: %+v", pack.PierCode)
	}
	if pack.PierCode.Artifact == nil || pack.PierCode.Artifact.SHA256 != hash {
		t.Fatalf("pier_code.artifact not parsed: %+v", pack.PierCode.Artifact)
	}
	if err := ValidatePack(pack); err != nil {
		t.Fatalf("pack with artifact should validate: %v", err)
	}
	if !strings.Contains(string(canonical), `"pier_code"`) || !strings.Contains(string(canonical), hash) {
		t.Fatalf("pier_code artifact not in canonical json: %s", canonical)
	}

	// An unknown key under pier_code.artifact is rejected (DisallowUnknownFields
	// reaches nested structs).
	bad := []byte(`{"name":"c","targets":[{"ra":1,"dec":2,"filters":["R"],"exposure_sec":30,"frame_count":1}],"pier_code":{"enabled":true,"artifact":{"sha256":"` + hash + `","url":"https://x/y","rootkit":true}}}`)
	if _, _, err := CanonicalPackJSON(bad); err == nil {
		t.Fatal("unknown pier_code.artifact key should be rejected")
	}
}
