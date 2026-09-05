package main

import (
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/saucepan/hotpath/shared/coverage"
)

// GET /api/v1/campaigns/{id}/coverage/status — realized coverage KPIs (#397/#221).
func handleCampaignCoverageStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx, cancel := campaignCtx(r.Context())
	defer cancel()

	claims, ok := mustClaims(w, r, "authentication required")
	if !ok {
		return
	}
	if err := assertCampaignOwner(ctx, id, claims.UserID); err != nil {
		writeError(w, 403, "Forbidden")
		return
	}

	intent, _, _, _, err := loadCoverageTarget(ctx, id)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}

	var packJSON []byte
	_ = db.QueryRow(ctx, `SELECT pack_json FROM campaigns WHERE id = $1::uuid`, id).Scan(&packJSON)
	var coveragePlan any
	if len(packJSON) > 0 {
		var pack map[string]any
		if json.Unmarshal(packJSON, &pack) == nil {
			coveragePlan = pack["coverage_plan"]
		}
	}

	binMin := 15.0
	if q := r.URL.Query().Get("bin_minutes"); q != "" {
		if v, err := strconv.ParseFloat(q, 64); err == nil && v > 0 {
			binMin = v
		}
	}

	rows, err := db.Query(ctx, `
		SELECT fg.telescope_id,
		       COALESCE(t.site_longitude, 0),
		       COALESCE(fg.created_at, NOW()) AS ts,
		       COALESCE(fg.stack_eligible, false)
		FROM frame_grades fg
		JOIN tasks tk ON tk.id = fg.task_id
		LEFT JOIN telescopes t ON t.telescope_id = fg.telescope_id
		WHERE tk.campaign_id = $1::uuid
		ORDER BY ts ASC
	`, id)
	if err != nil {
		writeError(w, 500, "Database error")
		return
	}
	defer rows.Close()

	type sample struct {
		tel string
		lon float64
		ts  time.Time
	}
	var samples []sample
	counts := map[string]int{}
	lons := map[string]float64{}
	for rows.Next() {
		var tel string
		var lon float64
		var ts time.Time
		var eligible bool
		if err := rows.Scan(&tel, &lon, &ts, &eligible); err != nil {
			writeError(w, 500, "Database error")
			return
		}
		_ = eligible
		if tel == "" {
			continue
		}
		samples = append(samples, sample{tel: tel, lon: lon, ts: ts})
		counts[tel]++
		if lon != 0 {
			lons[tel] = lon
		}
	}

	contributors := make([]map[string]any, 0, len(counts))
	for tid, n := range counts {
		entry := map[string]any{"telescope_id": tid, "frames": n}
		if lon, ok := lons[tid]; ok {
			entry["longitude"] = lon
		}
		contributors = append(contributors, entry)
	}
	sort.Slice(contributors, func(i, j int) bool {
		return contributors[i]["telescope_id"].(string) < contributors[j]["telescope_id"].(string)
	})

	var lonList []float64
	for _, lon := range lons {
		lonList = append(lonList, lon)
	}
	lonSpan := coverage.CircularLonSpanDeg(lonList)

	var realizedGap *float64
	duty := 0.0
	timeBasis := "frame_grades.created_at"
	status := "insufficient_data"
	reasons := []string{}

	if len(samples) >= 2 {
		maxGap := 0.0
		for i := 1; i < len(samples); i++ {
			g := samples[i].ts.Sub(samples[i-1].ts).Minutes()
			if g > maxGap {
				maxGap = g
			}
		}
		realizedGap = &maxGap
		start := samples[0].ts
		end := samples[len(samples)-1].ts
		if end.After(start) {
			bins := int(math.Ceil(end.Sub(start).Minutes() / binMin))
			if bins < 1 {
				bins = 1
			}
			filled := map[int]bool{}
			for _, s := range samples {
				idx := int(s.ts.Sub(start).Minutes() / binMin)
				if idx < 0 {
					idx = 0
				}
				if idx >= bins {
					idx = bins - 1
				}
				filled[idx] = true
			}
			duty = float64(len(filled)) / float64(bins)
		} else {
			duty = 1.0
		}
		status = "ok"
		if intent.MaxGapMin > 0 && maxGap > float64(intent.MaxGapMin) {
			reasons = append(reasons, "realized_max_gap_min exceeds intent")
			if intent.IsHard() {
				status = "failed"
			} else {
				status = "degraded"
			}
		}
	} else if len(samples) == 1 {
		duty = 1.0
		status = "insufficient_data"
		reasons = append(reasons, "need ≥2 frames for gap metric")
	} else {
		reasons = append(reasons, "no frames yet")
	}

	if intent.MinSites > 0 && len(counts) < intent.MinSites {
		reasons = append(reasons, "min_sites not met")
		if intent.IsHard() {
			status = "failed"
		} else if status == "ok" {
			status = "degraded"
		}
	}
	if intent.MinLongitudeSpanDeg > 0 && lonSpan+1e-9 < intent.MinLongitudeSpanDeg && len(lonList) >= 2 {
		reasons = append(reasons, "min_longitude_span_deg not met")
		if intent.IsHard() {
			status = "failed"
		} else if status == "ok" {
			status = "degraded"
		}
	}

	vs := map[string]any{
		"max_gap_intent_min":     intent.MaxGapMin,
		"min_sites":              intent.MinSites,
		"min_longitude_span_deg": intent.MinLongitudeSpanDeg,
		"mode":                   intent.Mode,
	}
	if realizedGap != nil && intent.MaxGapMin > 0 {
		vs["max_gap_excess_min"] = *realizedGap - float64(intent.MaxGapMin)
	}

	writeJSON(w, 200, map[string]any{
		"campaign_id":             id,
		"mode":                    intent.Mode,
		"status":                  status,
		"gate_reasons":            reasons,
		"contributing_telescopes": contributors,
		"longitude_span_deg":      lonSpan,
		"realized_max_gap_min":    realizedGap,
		"duty_cycle":              duty,
		"bin_minutes":             binMin,
		"time_basis":              timeBasis,
		"vs_intent":               vs,
		"coverage_plan":           coveragePlan,
		"coverage":                intent,
		"note":                    "Not a science oracle. Gaps use delivery/grade timestamps; geometric night not modeled in v1 (#397).",
	})
}

// GET /api/v1/fleet/sites — geography + optics inventory for coverage planning (#222 slice / #397).
func handleFleetSites(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := campaignCtx(r.Context())
	defer cancel()

	if _, ok := mustClaims(w, r, "authentication required"); !ok {
		return
	}

	rows, err := db.Query(ctx, `
		SELECT telescope_id,
		       site_latitude,
		       site_longitude,
		       aperture_mm,
		       available_filters,
		       COALESCE(is_emulator, false),
		       COALESCE(is_active, true)
		FROM telescopes
		ORDER BY telescope_id
	`)
	if err != nil {
		writeError(w, 500, "Database error")
		return
	}
	defer rows.Close()

	sites := make([]map[string]any, 0)
	for rows.Next() {
		var id string
		var lat, lon, aperture *float64
		var filters []string
		var emu, active bool
		if err := rows.Scan(&id, &lat, &lon, &aperture, &filters, &emu, &active); err != nil {
			writeError(w, 500, "Database error")
			return
		}
		entry := map[string]any{
			"telescope_id": id,
			"is_emulator":  emu,
			"enabled":      active,
		}
		if lat != nil {
			entry["latitude"] = *lat
		}
		if lon != nil {
			entry["longitude"] = *lon
		}
		if aperture != nil {
			entry["aperture_mm"] = *aperture
		}
		if filters != nil {
			entry["available_filters"] = filters
		} else {
			entry["available_filters"] = []string{}
		}
		sites = append(sites, entry)
	}
	writeJSON(w, 200, map[string]any{
		"sites": sites,
		"count": len(sites),
		"note":  "Geography + optics inventory only; timing/photometric class registry remains on #222.",
	})
}
