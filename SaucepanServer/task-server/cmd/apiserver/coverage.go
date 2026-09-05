package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/saucepan/hotpath/shared/campaign"
	"github.com/saucepan/hotpath/shared/coverage"
)

// POST /api/v1/campaigns/{id}/coverage — set coverage intent (researcher_approved).
func handleSetCampaignCoverage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body coverage.Intent
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, "Invalid JSON")
		return
	}
	body = body.Normalize()

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

	if err := persistCampaignCoverage(ctx, id, body); err != nil {
		log.Printf("set coverage: %v", err)
		writeError(w, 500, "Database error")
		return
	}
	emitCampaignUpdate(ctx, id, "coverage.updated", "Coverage intent updated", nil, map[string]any{
		"enabled":    body.Enabled,
		"n_main":     body.NMain,
		"redundancy": body.Redundancy,
	})
	writeJSON(w, 200, map[string]any{"campaign_id": id, "coverage": body})
}

// POST /api/v1/campaigns/{id}/coverage/preview — read-only greedy plan (no mutation).
func handlePreviewCampaignCoverage(w http.ResponseWriter, r *http.Request) {
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

	intent, allowEmu, targetRA, targetDec, err := loadCoverageContext(ctx, id, r)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	sites, err := loadCoverageSites(ctx)
	if err != nil {
		writeError(w, 500, "Database error")
		return
	}
	plan := coverage.GreedyFill(
		intent, sites, targetRA, targetDec,
		coverage.DefaultFactors(),
		allowEmu,
	)
	writeJSON(w, 200, map[string]any{
		"campaign_id":  id,
		"coverage":     intent,
		"plan":         plan,
		"preferred":    coverage.PreferredIDs(plan),
		"gate_status":  plan.GateStatus,
		"gate_reasons": plan.GateReasons,
		"note":         "preview only — does not mutate assignment; factors: geometry, cohort, weather (Open-Meteo), reliability",
	})
}

// POST /api/v1/campaigns/{id}/coverage/apply — persist intent + open handoff windows on tasks.
func handleApplyCampaignCoverage(w http.ResponseWriter, r *http.Request) {
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

	var body coverage.Intent
	gotBody := false
	if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
		gotBody = true
		body = body.Normalize()
	} else {
		stored, _, _, _, err := loadCoverageTarget(ctx, id)
		if err != nil {
			writeError(w, 400, err.Error())
			return
		}
		body = stored
	}
	if gotBody {
		if err := persistCampaignCoverage(ctx, id, body); err != nil {
			log.Printf("apply coverage persist: %v", err)
			writeError(w, 500, "Database error")
			return
		}
	}

	_, allowEmu, targetRA, targetDec, err := loadCoverageTarget(ctx, id)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	sites, err := loadCoverageSites(ctx)
	if err != nil {
		writeError(w, 500, "Database error")
		return
	}
	plan := coverage.GreedyFill(
		body, sites, targetRA, targetDec,
		coverage.DefaultFactors(),
		allowEmu,
	)

	if body.IsHard() && body.Enabled && plan.GateStatus == "failed" {
		writeJSON(w, 409, map[string]any{
			"error":        "coverage gates failed in hard mode",
			"campaign_id":  id,
			"coverage":     body,
			"plan":         plan,
			"gate_status":  plan.GateStatus,
			"gate_reasons": plan.GateReasons,
		})
		return
	}

	if err := persistCampaignCoveragePlan(ctx, id, body, plan); err != nil {
		log.Printf("apply coverage plan persist: %v", err)
		writeError(w, 500, "Database error")
		return
	}

	updated := 0
	if body.Enabled && plan.GateStatus != "failed" {
		// Session leg (~4h); CoverageActive keeps planned urgency continuously (#84).
		end := time.Now().UTC().Add(4 * time.Hour)
		lead := 5400
		tag, err := db.Exec(ctx, `
			UPDATE tasks
			SET scheduled_end_at = $1, handoff_lead_seconds = $2, updated_at = NOW()
			WHERE campaign_id = $3::uuid
			  AND status NOT IN ('completed', 'superseded')
		`, end, lead, id)
		if err != nil {
			log.Printf("apply coverage handoff: %v", err)
			writeError(w, 500, "Database error")
			return
		}
		updated = int(tag.RowsAffected())
	} else if !body.Enabled {
		_, _ = db.Exec(ctx, `
			UPDATE tasks
			SET scheduled_end_at = NULL, handoff_lead_seconds = NULL, updated_at = NOW()
			WHERE campaign_id = $1::uuid
			  AND status NOT IN ('completed', 'superseded')
		`, id)
	}

	primaryIDs := coverage.SiteIDs(plan.Primary)
	redundantIDs := coverage.SiteIDs(plan.Redundant)
	evtType := "coverage.applied"
	evtMsg := "Coverage plan applied"
	if plan.GateStatus == "degraded" {
		evtType = "coverage.degraded"
		evtMsg = "Coverage plan applied with degraded gates"
	} else if plan.GateStatus == "failed" {
		evtType = "coverage.failed"
		evtMsg = "Coverage plan failed gates"
	}
	emitCampaignUpdate(ctx, id, evtType, evtMsg, nil, map[string]any{
		"enabled":       body.Enabled,
		"n_main":        body.NMain,
		"redundancy":    body.Redundancy,
		"mode":          body.Mode,
		"primary":       primaryIDs,
		"redundant":     redundantIDs,
		"gate_status":   plan.GateStatus,
		"gate_reasons":  plan.GateReasons,
		"tasks_updated": updated,
	})
	writeJSON(w, 200, map[string]any{
		"campaign_id":   id,
		"coverage":      body,
		"plan":          plan,
		"preferred":     coverage.PreferredIDs(plan),
		"gate_status":   plan.GateStatus,
		"gate_reasons":  plan.GateReasons,
		"tasks_updated": updated,
	})
}

// IntentFromCampaignPack maps pack coverage intent into the planner Intent (#397).
func IntentFromCampaignPack(c *campaign.CoverageIntent) coverage.Intent {
	if c == nil {
		return coverage.DefaultIntent()
	}
	return coverage.Intent{
		Enabled:             c.Enabled,
		NMain:               c.NMain,
		Redundancy:          c.Redundancy,
		MaxGapMin:           c.MaxGapMin,
		MaxSites:            c.MaxSites,
		Mode:                c.Mode,
		PreferredSites:      append([]string{}, c.PreferredSites...),
		MinSites:            c.MinSites,
		MinLongitudeSpanDeg: c.MinLongitudeSpanDeg,
	}.Normalize()
}

func persistCampaignCoverage(ctx context.Context, campaignID string, intent coverage.Intent) error {
	var packJSON []byte
	err := db.QueryRow(ctx, `SELECT pack_json FROM campaigns WHERE id = $1::uuid`, campaignID).Scan(&packJSON)
	if err != nil {
		return err
	}
	var pack map[string]any
	if len(packJSON) > 0 {
		_ = json.Unmarshal(packJSON, &pack)
	}
	if pack == nil {
		pack = map[string]any{}
	}
	pack["coverage"] = intent
	if !intent.Enabled {
		delete(pack, "coverage_plan")
	}
	raw, err := json.Marshal(pack)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `UPDATE campaigns SET pack_json = $1::jsonb WHERE id = $2::uuid`, string(raw), campaignID)
	return err
}

func persistCampaignCoveragePlan(ctx context.Context, campaignID string, intent coverage.Intent, plan coverage.Plan) error {
	var packJSON []byte
	err := db.QueryRow(ctx, `SELECT pack_json FROM campaigns WHERE id = $1::uuid`, campaignID).Scan(&packJSON)
	if err != nil {
		return err
	}
	var pack map[string]any
	if len(packJSON) > 0 {
		_ = json.Unmarshal(packJSON, &pack)
	}
	if pack == nil {
		pack = map[string]any{}
	}
	pack["coverage"] = intent
	if intent.Enabled {
		pack["coverage_plan"] = map[string]any{
			"primary":                  coverage.SiteIDs(plan.Primary),
			"redundant":                coverage.SiteIDs(plan.Redundant),
			"estimated_coverage_hours": plan.CoverageH,
			"estimated_max_gap_min":    plan.MaxGapMin,
			"longitude_span_deg":       plan.LongitudeSpanDeg,
			"gate_status":              plan.GateStatus,
			"gate_reasons":             plan.GateReasons,
			"updated_at":               time.Now().UTC().Format(time.RFC3339),
		}
	} else {
		delete(pack, "coverage_plan")
	}
	raw, err := json.Marshal(pack)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `UPDATE campaigns SET pack_json = $1::jsonb WHERE id = $2::uuid`, string(raw), campaignID)
	return err
}

func loadCoverageContext(ctx context.Context, campaignID string, r *http.Request) (coverage.Intent, bool, float64, float64, error) {
	intent, allowEmu, ra, dec, err := loadCoverageTarget(ctx, campaignID)
	if err != nil {
		return coverage.Intent{}, false, 0, 0, err
	}
	if r != nil && r.Body != nil && r.ContentLength != 0 {
		var override coverage.Intent
		if err := json.NewDecoder(r.Body).Decode(&override); err == nil {
			if override.NMain > 0 || override.Enabled || override.Redundancy || override.MaxSites > 0 {
				intent = override.Normalize()
			}
		}
	}
	return intent, allowEmu, ra, dec, nil
}

func loadCoverageTarget(ctx context.Context, campaignID string) (coverage.Intent, bool, float64, float64, error) {
	var packJSON []byte
	var testOnly bool
	err := db.QueryRow(ctx, `
		SELECT pack_json, test_only FROM campaigns WHERE id = $1::uuid
	`, campaignID).Scan(&packJSON, &testOnly)
	if err != nil {
		return coverage.Intent{}, false, 0, 0, err
	}
	pack, err := campaign.ParsePack(packJSON)
	if err != nil {
		return coverage.Intent{}, false, 0, 0, err
	}
	intent := coverage.DefaultIntent()
	if pack.Coverage != nil {
		intent = IntentFromCampaignPack(pack.Coverage)
	}
	var ra, dec float64
	if len(pack.Targets) > 0 {
		ra, dec = pack.Targets[0].RA, pack.Targets[0].Dec
	}
	return intent, testOnly || pack.TestOnly, ra, dec, nil
}

func loadCoverageSites(ctx context.Context) ([]coverage.Site, error) {
	rows, err := db.Query(ctx, `
		SELECT telescope_id,
		       COALESCE(site_latitude, 0),
		       COALESCE(site_longitude, 0),
		       COALESCE(power, 0.5),
		       COALESCE(is_emulator, false),
		       COALESCE((reputation_stats->>'reliability_score')::float, 0.8)
		FROM telescopes
		WHERE is_active = true
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sites []coverage.Site
	for rows.Next() {
		var s coverage.Site
		var power float64
		if err := rows.Scan(&s.TelescopeID, &s.Lat, &s.Lon, &power, &s.IsEmulator, &s.Reliability); err != nil {
			return nil, err
		}
		s.CohortScore = power
		if s.CohortScore <= 0 {
			s.CohortScore = 0.5
		}
		if s.Reliability <= 0 {
			s.Reliability = 0.5
		}
		sites = append(sites, s)
	}
	coverage.EnrichWeather(sites)
	return sites, nil
}
