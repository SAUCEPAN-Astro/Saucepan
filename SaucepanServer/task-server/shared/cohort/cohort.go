// cohort.go — Deterministic vector-based telescope clustering
//
// Each telescope is represented as a weighted feature vector.
// Cosine similarity between vectors determines how compatible
// two telescopes are for heterogeneous stacking.
//
// Dimensions (all normalized 0→1, higher = bigger/broader):
//   0: pixel_scale_log    — log(pixel_scale_arcsec_px) across 0.1"–10" range
//   1: fov_log            — log(sqrt(width*height) arcmin) across 0.1'–300' range
//   2: sensitivity        — aperture_mm² * pixel_scale_arcsec_px⁻², normalized
//   3: seeing_arcsec      — 0.5"–5" range
//   4: mount_type         — 0 = alt-az, 1 = equatorial (binary)
//
// Weights (must sum to 1):
//   pixel_scale_log:  0.40
//   fov_log:          0.20
//   sensitivity:      0.15
//   seeing_arcsec:    0.10
//   mount_type:       0.05
//   filter_jaccard:   0.10 (computed separately, not in vector)

package cohort

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"sort"
	"strings"
)

// ── Constants ──────────────────────────────────────────────────────────

const NDims = 5 // number of feature dimensions

var DefaultWeights = [NDims]float64{0.40, 0.20, 0.15, 0.10, 0.05}

// Physical bounds for normalization
// These are reasonable min/max values for consumer-grade astro cameras.
const (
	PixelScaleMin  = 0.1   // "/px (unlikely to be sharper)
	PixelScaleMax  = 10.0  // "/px (unlikely to be wider)
	FOVMin         = 0.1   // arcmin (tiny FOV)
	FOVMax         = 300.0 // arcmin = 5° (ultra-wide)
	SensitivityMax = 1e6   // aperture² * scale⁻², arbitrary cap
	SeeingMin      = 0.5   // arcsec (diffraction-limited)
	SeeingMax      = 5.0   // arcsec (terrible seeing)
)

// ── Types ──────────────────────────────────────────────────────────────

// Vector is a normalized 5-dimensional feature vector [0–1].
type Vector [NDims]float64

// TelescopeSpec holds the raw parameters needed to compute a vector.
type TelescopeSpec struct {
	ApertureMM      float64
	FocalLengthMM   float64
	PixelSizeUM     float64
	FOVWidthArcmin  float64
	FOVHeightArcmin float64
	SeeingArcsec    float64
	MountType       int // 0=altaz, 1=eq
	Filters         []string
}

// MatchResult holds the output of a cohort search.
type MatchResult struct {
	TelescopeID string
	Similarity  float64
	Compatible  bool
}

// ── Vector computation ─────────────────────────────────────────────

// clamp normalises v within [lo, hi].
func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ComputeVector builds a normalised 5-d feature vector from a telescope spec.
func ComputeVector(spec TelescopeSpec) Vector {
	var v Vector

	// 0. Pixel scale ("/px) — log-normalised
	pxScale := (spec.PixelSizeUM / spec.FocalLengthMM) * 206.265
	if pxScale < PixelScaleMin {
		pxScale = PixelScaleMin
	}
	v[0] = clamp(
		(math.Log(pxScale)-math.Log(PixelScaleMin))/
			(math.Log(PixelScaleMax)-math.Log(PixelScaleMin)),
		0, 1,
	)

	// 1. FOV (arcmin, geometric mean) — log-normalised
	fovGeom := math.Sqrt(spec.FOVWidthArcmin * spec.FOVHeightArcmin)
	if fovGeom < FOVMin {
		fovGeom = FOVMin
	}
	v[1] = clamp(
		(math.Log(fovGeom)-math.Log(FOVMin))/
			(math.Log(FOVMax)-math.Log(FOVMin)),
		0, 1,
	)

	// 2. Sensitivity — proportional to aperture² × (px_scale)⁻²
	// Larger aperture + smaller pixels = more light per sky-area.
	sensitivity := (spec.ApertureMM * spec.ApertureMM) / (pxScale * pxScale)
	v[2] = clamp(sensitivity/SensitivityMax, 0, 1)

	// 3. Seeing / typical FWHM (arcsec)
	v[3] = clamp(
		(spec.SeeingArcsec-SeeingMin)/(SeeingMax-SeeingMin),
		0, 1,
	)

	// 4. Mount type — binary (0 = alt-az, 1 = equatorial)
	v[4] = clamp(float64(spec.MountType), 0, 1)

	return v
}

// ── Cosine similarity (weighted) ──────────────────────────────────────

// WeightedCosine returns the cosine similarity between two vectors, applying
// per-dimension weights. Returns 0–1 (1 = identical).
func WeightedCosine(a, b Vector, weights [NDims]float64) float64 {
	var dot, magA, magB float64
	for i := 0; i < NDims; i++ {
		wa := a[i] * weights[i]
		wb := b[i] * weights[i]
		dot += wa * wb
		magA += wa * wa
		magB += wb * wb
	}
	if magA == 0 || magB == 0 {
		return 0
	}
	return dot / (math.Sqrt(magA) * math.Sqrt(magB))
}

// ── Filter compatibility (Jaccard index) ─────────────────────────────

// JaccardFilterScore returns the Jaccard similarity between two filter sets.
// 1.0 = identical, 0.0 = no overlap.
func JaccardFilterScore(a, b []string) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1.0 // no filters = anything goes
	}
	set := make(map[string]struct{}, len(a))
	for _, f := range a {
		set[f] = struct{}{}
	}
	intersection := 0
	for _, f := range b {
		if _, ok := set[f]; ok {
			intersection++
		}
	}
	union := len(a) + len(b) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

// ── Combined compatibility score ──────────────────────────────────────

// Compatibility combines vector cosine similarity and filter Jaccard
// into a single 0–1 score.
//
//	score = 0.90 * cosine + 0.10 * filter_jaccard
func Compatibility(aVec Vector, bVec Vector, aFilters, bFilters []string, weights [NDims]float64) float64 {
	cosine := WeightedCosine(aVec, bVec, weights)
	filterScore := JaccardFilterScore(aFilters, bFilters)
	return 0.90*cosine + 0.10*filterScore
}

// ── Adaptive threshold ────────────────────────────────────────────────

// AdaptiveThreshold returns the optimal binary-search step based on
// the current cohort size.
//
// Target: 3–8 compatible telescopes per cohort.
// If size < 3: lower threshold (return -0.05)
// If size > 8: raise threshold (return +0.05)
// If in range: no change (return 0.0)
func AdaptiveStep(cohortSize int) float64 {
	switch {
	case cohortSize < 3:
		return -0.05
	case cohortSize > 8:
		return +0.05
	default:
		return 0.0
	}
}

// ClampThreshold ensures threshold stays within [0.50, 0.98].
func ClampThreshold(t float64) float64 {
	switch {
	case t < 0.50:
		return 0.50
	case t > 0.98:
		return 0.98
	default:
		return t
	}
}

// DefaultBandWidths — generic tolerance for cohort-fill fallback and scarcity substitute counts.
// Index order: [pixel_scale_log, fov_log, sensitivity, seeing_arcsec, mount_type]
var DefaultBandWidths = [NDims]float64{0.30, 0.50, 0.50, 0.30, 0.0}

// BandProfiles — science_band-keyed tolerance overrides for cohort-fill.
var BandProfiles = map[string][NDims]float64{
	"planetary":  {0.15, 0.30, 0.40, 0.15, 0.0},
	"wide_field": {0.50, 0.70, 0.60, 0.50, 0.0},
}

func BandWidthsFor(scienceBand string) [NDims]float64 {
	if w, ok := BandProfiles[scienceBand]; ok {
		return w
	}
	return DefaultBandWidths
}

// PassesBands checks each vector dimension independently (non-compensatory).
func PassesBands(anchor, candidate Vector, widths [NDims]float64) bool {
	for i := 0; i < NDims; i++ {
		if widths[i] <= 0 {
			if anchor[i] != candidate[i] {
				return false
			}
			continue
		}
		if math.Abs(candidate[i]-anchor[i]) > widths[i] {
			return false
		}
	}
	return true
}

// AdaptiveBandWidth adjusts band widths based on current cohort size.
func AdaptiveBandWidth(base [NDims]float64, cohortSize int) [NDims]float64 {
	step := AdaptiveStep(cohortSize)
	var out [NDims]float64
	for i := range base {
		if base[i] <= 0 {
			out[i] = base[i]
			continue
		}
		out[i] = base[i] - step
		if out[i] < 0.05 {
			out[i] = 0.05
		}
		if out[i] > 0.95 {
			out[i] = 0.95
		}
	}
	return out
}

// ── Cohort search ────────────────────────────────────────────────────

// Candidate is a scored telescope match.
type Candidate struct {
	TelescopeID string
	Score       float64
}

// FindCohort returns telescopes compatible with the given one, sorted
// descending by compatibility score.
//
// It applies both cosine+filter compatibility and the adaptive threshold.
func FindCohort(
	myID string,
	myVec Vector,
	myFilters []string,
	allTelescopes []TelescopeEntry,
	threshold float64,
	weights [NDims]float64,
) []Candidate {
	var candidates []Candidate

	for _, other := range allTelescopes {
		if other.ID == myID {
			continue // skip self
		}
		score := Compatibility(myVec, other.Vector, myFilters, other.Filters, weights)
		// Apply reliability penalty: low-reliability scopes need higher compatibility
		// to be in the same cohort (multiplier: 0.5–1.0 based on reliability score)
		reliabilityMult := 0.5 + (other.Reliability * 0.5)
		score = score * reliabilityMult
		if score >= threshold {
			candidates = append(candidates, Candidate{
				TelescopeID: other.ID,
				Score:       score,
			})
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Score > candidates[j].Score // desc
	})

	return candidates
}

// TelescopeEntry is a lightweight struct used by FindCohort.
type TelescopeEntry struct {
	ID          string
	Vector      Vector
	Filters     []string
	Reliability float64 // 0-1 reputation score from network history
}

// ── Cohort hash ──────────────────────────────────────────────────────

// CohortHash produces a deterministic string key for a set of telescope IDs.
// Used as a Redis key for active cohort groups: order-independent (IDs are
// sorted before hashing) and collision-resistant across distinct ID sets.
//
// Returns "" for an empty slice — callers must treat that as "no cohort"
// rather than using it as a Redis key.
func CohortHash(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	sorted := make([]string, len(ids))
	copy(sorted, ids)
	sort.Strings(sorted)
	sum := sha256.Sum256([]byte(strings.Join(sorted, "\x00")))
	return hex.EncodeToString(sum[:])
}
