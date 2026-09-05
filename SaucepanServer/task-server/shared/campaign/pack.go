package campaign

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/saucepan/hotpath/shared/wire"
)

// ErrHookNotApproved is returned when a compute-placement hook lacks operator approval.
var ErrHookNotApproved = errors.New("campaign science hook requires operator approval for compute placement")

// CompStar is an optional comparison star for campaign photometry.
type CompStar struct {
	RA   float64  `json:"ra"`
	Dec  float64  `json:"dec"`
	Mag  *float64 `json:"mag,omitempty"`
	Band *string  `json:"band,omitempty"`
}

// ProductIntent declares science product / time resolution (#422).
// Default (nil / empty mode) is per_frame: calibrated frames + photometry table, not /v1/stack.
type ProductIntent struct {
	Mode          string `json:"mode,omitempty"`            // per_frame | time_bin | stack
	TimeBinFrames int    `json:"time_bin_frames,omitempty"` // required when mode=time_bin (≥2)
}

// Pack matches SaucepanServer/contracts/campaign_pack.schema.json.
type Pack struct {
	Name          string          `json:"name"`
	TestOnly      bool            `json:"test_only"`
	Description   string          `json:"description,omitempty"`
	HookImageRef  string          `json:"hook_image_ref,omitempty"`
	HookPlacement string          `json:"hook_placement,omitempty"`
	CompStars     []CompStar      `json:"comp_stars,omitempty"`
	Product       *ProductIntent  `json:"product,omitempty"`
	Coverage      *CoverageIntent `json:"coverage,omitempty"`
	Season        *SeasonIntent   `json:"season,omitempty"`
	PierCode      *PierCodeIntent `json:"pier_code,omitempty"`
	Targets       []PackTarget    `json:"targets"`
}

// PierCodeIntent opts a campaign into shipping sandboxed researcher code to
// its piers (#470 step 3 / #516). Absent = no on-pier code at all. Present
// with Enabled but a nil/empty Actions map = the read+board default
// (wire.DefaultPierCodeGrants). Each Actions key must be a wire.PierCodeActions
// name; value true grants it. Nothing here makes code run on a pier by
// itself — the pier still needs a local consent record (#517) and the
// campaign kill switch (#520) unset.
type PierCodeIntent struct {
	Enabled bool            `json:"enabled"`
	Actions map[string]bool `json:"actions,omitempty"`
	// Artifact is the compiled researcher module to run (#470 step 5 / #518):
	// content hash + fetch URL. Absent = the campaign enabled the capability
	// surface but has no code to ship yet (grants alone, no runner invocation).
	Artifact *wire.PierCodeRef `json:"artifact,omitempty"`
}

// CoverageIntent is optional continuous-coverage config (default off). See #84/#397.
type CoverageIntent struct {
	Enabled             bool     `json:"enabled"`
	NMain               int      `json:"n_main,omitempty"`
	Redundancy          bool     `json:"redundancy,omitempty"`
	MaxGapMin           int      `json:"max_gap_min,omitempty"`
	MaxSites            int      `json:"max_sites,omitempty"`
	Mode                string   `json:"mode,omitempty"` // soft (default) | hard
	PreferredSites      []string `json:"preferred_sites,omitempty"`
	MinSites            int      `json:"min_sites,omitempty"`
	MinLongitudeSpanDeg float64  `json:"min_longitude_span_deg,omitempty"`
}

// SeasonIntent is KPI/window metadata for continuous/sparse/ToO seasons (#397).
type SeasonIntent struct {
	Kind            string   `json:"kind,omitempty"` // continuous | sparse | too
	Urgency         string   `json:"urgency,omitempty"`
	CadenceGoalMin  int      `json:"cadence_goal_min,omitempty"`
	WindowStart     *string  `json:"window_start,omitempty"`
	WindowEnd       *string  `json:"window_end,omitempty"`
	TargetDutyCycle *float64 `json:"target_duty_cycle,omitempty"`
	Designation     string   `json:"designation,omitempty"`
	BreakNotes      string   `json:"break_notes,omitempty"`
}

// SaturationHint is bright-target ops guidance for pier operators.
type SaturationHint struct {
	MaxExposureSec *float64 `json:"max_exposure_sec,omitempty"`
	Strategy       string   `json:"strategy,omitempty"` // short | defocus | nd | manual
	Notes          string   `json:"notes,omitempty"`
}

// PackTarget is one observation target in a campaign pack.
type PackTarget struct {
	RA             float64         `json:"ra"`
	Dec            float64         `json:"dec"`
	Magnitude      *float64        `json:"magnitude,omitempty"`
	Filters        []string        `json:"filters"`
	ExposureSec    float64         `json:"exposure_sec"`
	FrameCount     int             `json:"frame_count"`
	CadenceGoalMin int             `json:"cadence_goal_min,omitempty"`
	Saturation     *SaturationHint `json:"saturation,omitempty"`
}

// TaskSpec is one expanded task row (one filter per task).
type TaskSpec struct {
	Name                         string
	TargetRA                     float64
	TargetDec                    float64
	RequiredFilters              []string
	IntegrationTime              float64
	NormalizedIntegrationBudgetS float64
	AllowEmulator                bool
	ProductMode                  string
	TargetMagnitude              *float64
}

// clientPackKeys are top-level keys allowed in researcher-submitted packs
// (mirrors SaucepanServer/contracts/campaign_pack.schema.json).
var clientPackKeys = map[string]struct{}{
	"name":           {},
	"description":    {},
	"test_only":      {},
	"hook_image_ref": {},
	"hook_placement": {},
	"comp_stars":     {},
	"product":        {},
	"coverage":       {},
	"season":         {},
	"pier_code":      {},
	"targets":        {},
}

// serverPackKeys may appear in stored pack_json after server-side writes
// (e.g. coverage planner) but must not be accepted from client create bodies.
var serverPackKeys = map[string]struct{}{
	"coverage_plan": {},
}

// ParsePack decodes JSON into a Pack (unknown fields ignored — prefer CanonicalPackJSON for writes).
func ParsePack(raw json.RawMessage) (*Pack, error) {
	var p Pack
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid pack JSON: %w", err)
	}
	return &p, nil
}

// CanonicalPackJSON parses a client pack with unknown fields rejected and returns
// the pack plus canonical JSON suitable for campaigns.pack_json persistence.
func CanonicalPackJSON(raw json.RawMessage) (*Pack, []byte, error) {
	if err := validatePackObjectKeys(raw, false); err != nil {
		return nil, nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var p Pack
	if err := dec.Decode(&p); err != nil {
		return nil, nil, fmt.Errorf("invalid pack JSON: %w", err)
	}
	if tok, err := dec.Token(); err != io.EOF {
		if err != nil {
			return nil, nil, fmt.Errorf("invalid pack JSON: %w", err)
		}
		return nil, nil, fmt.Errorf("invalid pack JSON: trailing data %v", tok)
	}
	canonical, err := json.Marshal(&p)
	if err != nil {
		return nil, nil, fmt.Errorf("canonicalize pack: %w", err)
	}
	return &p, canonical, nil
}

// ValidateStoredPackJSON rejects unknown top-level keys in pack_json already in DB
// (client keys + server-authored coverage_plan). Nested unknown fields are caught
// when CanonicalPackJSON is used on create; publish re-checks top-level trust boundary.
func ValidateStoredPackJSON(raw json.RawMessage) error {
	return validatePackObjectKeys(raw, true)
}

func validatePackObjectKeys(raw json.RawMessage, allowServerKeys bool) error {
	if len(raw) == 0 {
		return fmt.Errorf("pack JSON is required")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return fmt.Errorf("invalid pack JSON: %w", err)
	}
	for key := range obj {
		if _, ok := clientPackKeys[key]; ok {
			continue
		}
		if allowServerKeys {
			if _, ok := serverPackKeys[key]; ok {
				continue
			}
		}
		return fmt.Errorf("unknown pack field %q", key)
	}
	return nil
}

// ValidatePack checks a pack is ready for publish/expand.
func ValidatePack(p *Pack) error {
	if p == nil {
		return fmt.Errorf("pack is required")
	}
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if len(p.Targets) == 0 {
		return fmt.Errorf("at least one target is required")
	}
	if err := ValidateProduct(p.Product); err != nil {
		return err
	}
	if p.Coverage != nil {
		mode := strings.TrimSpace(p.Coverage.Mode)
		if mode != "" && mode != "soft" && mode != "hard" {
			return fmt.Errorf("coverage.mode must be soft or hard")
		}
		if p.Coverage.MinLongitudeSpanDeg < 0 || p.Coverage.MinLongitudeSpanDeg > 360 {
			return fmt.Errorf("coverage.min_longitude_span_deg must be in [0, 360]")
		}
	}
	if p.Season != nil {
		kind := strings.TrimSpace(p.Season.Kind)
		if kind != "" && kind != "continuous" && kind != "sparse" && kind != "too" {
			return fmt.Errorf("season.kind must be continuous, sparse, or too")
		}
		urg := strings.TrimSpace(p.Season.Urgency)
		if urg != "" && urg != "normal" && urg != "elevated" && urg != "critical" {
			return fmt.Errorf("season.urgency must be normal, elevated, or critical")
		}
		if p.Season.TargetDutyCycle != nil {
			d := *p.Season.TargetDutyCycle
			if d < 0 || d > 1 {
				return fmt.Errorf("season.target_duty_cycle must be in [0, 1]")
			}
		}
	}
	if p.PierCode != nil {
		for action := range p.PierCode.Actions {
			if !wire.IsPierCodeAction(action) {
				return fmt.Errorf("pier_code.actions: unknown action %q", action)
			}
		}
		if p.PierCode.Artifact != nil {
			if err := p.PierCode.Artifact.Validate(); err != nil {
				return fmt.Errorf("pier_code.artifact: %w", err)
			}
			if p.PierCode.Artifact.URL == "" {
				return fmt.Errorf("pier_code.artifact: url is required (the pier has no other way to fetch it)")
			}
		}
	}
	for i, t := range p.Targets {
		if len(t.Filters) == 0 {
			return fmt.Errorf("target %d: filters required", i)
		}
		if t.ExposureSec <= 0 {
			return fmt.Errorf("target %d: exposure_sec must be > 0", i)
		}
		if t.FrameCount <= 0 {
			return fmt.Errorf("target %d: frame_count must be > 0", i)
		}
		if t.Saturation != nil {
			strat := strings.TrimSpace(t.Saturation.Strategy)
			if strat != "" && strat != "short" && strat != "defocus" && strat != "nd" && strat != "manual" {
				return fmt.Errorf("target %d: saturation.strategy must be short, defocus, nd, or manual", i)
			}
			if t.Saturation.MaxExposureSec != nil && t.ExposureSec > *t.Saturation.MaxExposureSec {
				return fmt.Errorf("target %d: exposure_sec exceeds saturation.max_exposure_sec", i)
			}
		}
	}
	return nil
}

// EffectivePierCodeGrants resolves the grant map a pier should receive for
// this pack (#516):
//   - pack does not enable pier_code            → nil (caller sends nothing; pier runs no code)
//   - enabled, no explicit Actions map          → wire.DefaultPierCodeGrants() (read + board only)
//   - enabled, explicit Actions map             → that map, unknown keys dropped
//
// An explicit map fully replaces the default — a campaign that lists actions
// has stated its whole intent, including which of read/board it wants.
func EffectivePierCodeGrants(p *Pack) map[string]bool {
	if p == nil || p.PierCode == nil || !p.PierCode.Enabled {
		return nil
	}
	if len(p.PierCode.Actions) == 0 {
		return wire.DefaultPierCodeGrants()
	}
	out := make(map[string]bool, len(p.PierCode.Actions))
	for action, allowed := range p.PierCode.Actions {
		if wire.IsPierCodeAction(action) {
			out[action] = allowed
		}
	}
	return out
}

// EffectivePierCodeArtifact is the researcher module reference a pier should
// fetch and run for this pack (#470 step 5 / #518), or nil when the pack does
// not enable pier_code or names no artifact. A campaign can enable the grant
// surface without shipping code yet — grants without an artifact just means no
// runner is invoked.
func EffectivePierCodeArtifact(p *Pack) *wire.PierCodeRef {
	if p == nil || p.PierCode == nil || !p.PierCode.Enabled || p.PierCode.Artifact == nil {
		return nil
	}
	if p.PierCode.Artifact.Validate() != nil {
		return nil
	}
	ref := *p.PierCode.Artifact
	return &ref
}

// SortedGrantList is the granted (value-true) action names, sorted — for
// showing a pier operator what a campaign is asking for (#517).
func SortedGrantList(grants map[string]bool) []string {
	var out []string
	for a, ok := range grants {
		if ok {
			out = append(out, a)
		}
	}
	sort.Strings(out)
	return out
}

// ValidateProduct checks product.mode / time_bin_frames (#422).
func ValidateProduct(prod *ProductIntent) error {
	if prod == nil {
		return nil
	}
	mode := strings.TrimSpace(prod.Mode)
	if mode == "" {
		mode = "per_frame"
	}
	switch mode {
	case "per_frame", "stack":
		if prod.TimeBinFrames != 0 {
			return fmt.Errorf("product.time_bin_frames is only valid when mode=time_bin")
		}
	case "time_bin":
		if prod.TimeBinFrames < 2 {
			return fmt.Errorf("product.time_bin_frames must be >= 2 when mode=time_bin")
		}
	default:
		return fmt.Errorf("product.mode must be per_frame, time_bin, or stack")
	}
	return nil
}

// NormalizedProductMode returns per_frame when product is unset (photometry default).
func NormalizedProductMode(prod *ProductIntent) string {
	if prod == nil {
		return "per_frame"
	}
	mode := strings.TrimSpace(prod.Mode)
	if mode == "" {
		return "per_frame"
	}
	return mode
}

// WantsStack reports whether the pack should call POST /v1/stack (depth campaigns only).
func WantsStack(prod *ProductIntent) bool {
	return NormalizedProductMode(prod) == "stack"
}

// HasScienceHook reports whether the pack declares a non-none science hook.
func HasScienceHook(p *Pack) bool {
	if p == nil {
		return false
	}
	if strings.TrimSpace(p.HookImageRef) != "" {
		return true
	}
	placement := normalizedHookPlacement(p.HookPlacement)
	return placement != "none"
}

func normalizedHookPlacement(placement string) string {
	placement = strings.TrimSpace(placement)
	if placement == "" {
		return "none"
	}
	return placement
}

// ValidateHookFields checks hook_image_ref / hook_placement consistency.
func ValidateHookFields(p *Pack) error {
	if p == nil || !HasScienceHook(p) {
		return nil
	}
	placement := normalizedHookPlacement(p.HookPlacement)
	switch placement {
	case "none", "edge", "compute":
	default:
		return fmt.Errorf("hook_placement must be edge, compute, or none")
	}
	ref := strings.TrimSpace(p.HookImageRef)
	if placement != "none" && ref == "" {
		return fmt.Errorf("hook_image_ref is required when hook_placement is %s", placement)
	}
	if ref != "" && placement == "none" {
		return fmt.Errorf("hook_placement must be edge or compute when hook_image_ref is set")
	}
	return nil
}

// ValidateHookPublish enforces operator approval for compute-placement hooks (MVP stub; no execution).
func ValidateHookPublish(p *Pack, hookApproved bool) error {
	if err := ValidateHookFields(p); err != nil {
		return err
	}
	if p == nil {
		return nil
	}
	if normalizedHookPlacement(p.HookPlacement) == "compute" && !hookApproved {
		return ErrHookNotApproved
	}
	return nil
}

// ExpandPack expands targets × filters into task specs (one filter per task).
// Budget is frame_count × exposure_sec in 2 m-equivalent seconds (reference telescope).
func ExpandPack(p *Pack) ([]TaskSpec, error) {
	if err := ValidatePack(p); err != nil {
		return nil, err
	}
	if err := ValidateHookFields(p); err != nil {
		return nil, err
	}
	var out []TaskSpec
	for _, t := range p.Targets {
		budget := float64(t.FrameCount) * t.ExposureSec
		for _, filt := range t.Filters {
			filt = strings.TrimSpace(filt)
			if filt == "" {
				continue
			}
			name := fmt.Sprintf("%s ra=%.4f dec=%.4f %s", p.Name, t.RA, t.Dec, filt)
			out = append(out, TaskSpec{
				Name:                         name,
				TargetRA:                     t.RA,
				TargetDec:                    t.Dec,
				RequiredFilters:              []string{filt},
				IntegrationTime:              t.ExposureSec,
				NormalizedIntegrationBudgetS: budget,
				AllowEmulator:                p.TestOnly,
				ProductMode:                  NormalizedProductMode(p.Product),
				TargetMagnitude:              t.Magnitude,
			})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no tasks produced from pack")
	}
	return out, nil
}
