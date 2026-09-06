package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/saucepan/hotpath/shared"
	"github.com/saucepan/hotpath/shared/campaign"
	"github.com/saucepan/hotpath/shared/coverage"
	"github.com/saucepan/hotpath/shared/weather"
	"go.uber.org/zap"
)

const (
	coverageSessionHours     = 4
	coverageHandoffLeadSec   = 5400
	defaultForecastInterval  = 30 * time.Minute
	defaultCoverageReplanInt = 30 * time.Minute
)

// rollCoverageSession advances scheduled_end_at so the next geographic handoff
// window opens near the end of this pier's coverage leg.
func rollCoverageSession(ctx context.Context, pool *pgxpool.Pool, taskID int, sugar *zap.SugaredLogger) {
	end := time.Now().UTC().Add(coverageSessionHours * time.Hour)
	lead := coverageHandoffLeadSec
	_, err := pool.Exec(ctx, `
		UPDATE tasks
		SET scheduled_end_at = $1, handoff_lead_seconds = $2, updated_at = NOW()
		WHERE id = $3 AND status NOT IN ('completed', 'superseded')
	`, end, lead, taskID)
	if err != nil {
		sugar.Warnw("coverage session roll failed", "task_id", taskID, "err", err)
	}
}

func coverageLoopInterval(envKey string, def time.Duration) time.Duration {
	v := os.Getenv(envKey)
	if v == "" {
		return def
	}
	min, err := strconv.Atoi(v)
	if err != nil || min <= 0 {
		return def
	}
	return time.Duration(min) * time.Minute
}

// startCoverageLoops runs Open-Meteo forecast refresh (#36) and coverage re-plan (#84).
func startCoverageLoops(ctx context.Context, pool *pgxpool.Pool, rdb *redis.Client, sugar *zap.SugaredLogger) {
	go func() {
		interval := coverageLoopInterval("FORECAST_REFRESH_MINUTES", defaultForecastInterval)
		sugar.Infow("forecast refresh loop started", "interval", interval.String())
		t := time.NewTicker(interval)
		defer t.Stop()
		refreshSiteForecasts(ctx, pool, rdb, sugar)
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				refreshSiteForecasts(ctx, pool, rdb, sugar)
			}
		}
	}()
	go func() {
		interval := coverageLoopInterval("COVERAGE_REPLAN_MINUTES", defaultCoverageReplanInt)
		sugar.Infow("coverage replan loop started", "interval", interval.String())
		t := time.NewTicker(interval)
		defer t.Stop()
		replanCoverageCampaigns(ctx, pool, sugar)
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				replanCoverageCampaigns(ctx, pool, sugar)
			}
		}
	}()
}

func refreshSiteForecasts(ctx context.Context, pool *pgxpool.Pool, rdb *redis.Client, sugar *zap.SugaredLogger) {
	rows, err := pool.Query(ctx, `
		SELECT telescope_id,
		       COALESCE(site_latitude, 0),
		       COALESCE(site_longitude, 0)
		FROM telescopes
		WHERE is_active = true
		  AND site_latitude IS NOT NULL
		  AND site_longitude IS NOT NULL
	`)
	if err != nil {
		sugar.Warnw("forecast query telescopes", "err", err)
		return
	}
	defer rows.Close()

	updated := 0
	for rows.Next() {
		var id string
		var lat, lon float64
		if err := rows.Scan(&id, &lat, &lon); err != nil {
			continue
		}
		if lat == 0 && lon == 0 {
			continue
		}
		snap := weather.Fetch(lat, lon)
		if !snap.OK {
			continue
		}
		_, err := pool.Exec(ctx, `
			INSERT INTO site_forecast_cache (
				telescope_id, latitude, longitude, cloud_cover, clearness,
				seeing_arcsec, wind_speed_ms, relative_humidity, source, raw_json, fetched_at
			) VALUES (
				$1, $2, $3, $4, $5, $6, $7, $8, 'open-meteo', $9::jsonb, NOW()
			)
			ON CONFLICT (telescope_id) DO UPDATE SET
				latitude = EXCLUDED.latitude,
				longitude = EXCLUDED.longitude,
				cloud_cover = EXCLUDED.cloud_cover,
				clearness = EXCLUDED.clearness,
				seeing_arcsec = EXCLUDED.seeing_arcsec,
				wind_speed_ms = EXCLUDED.wind_speed_ms,
				relative_humidity = EXCLUDED.relative_humidity,
				raw_json = EXCLUDED.raw_json,
				fetched_at = NOW()
		`, id, lat, lon, snap.CloudCover, snap.Clearness, snap.SeeingArcsec,
			snap.WindSpeedMs, snap.RelativeHumidity, stringOrEmptyJSON(snap.RawJSON))
		if err != nil {
			// Table may not exist until alembic upgrade — log once-level warn.
			sugar.Warnw("forecast cache upsert", "telescope_id", id, "err", err)
			continue
		}
		_, err = pool.Exec(ctx, `
			UPDATE telescopes SET median_seeing_arcsec = $1 WHERE telescope_id = $2
		`, snap.SeeingArcsec, id)
		if err != nil {
			sugar.Warnw("forecast seeing update", "telescope_id", id, "err", err)
			continue
		}
		if rdb != nil {
			key := fmt.Sprintf(shared.RedisNodeMeta, id)
			_ = rdb.HSet(ctx, key, "median_seeing_arcsec", snap.SeeingArcsec).Err()
		}
		updated++
	}
	if updated > 0 {
		sugar.Infow("forecast refresh complete", "updated", updated)
	}
}

func stringOrEmptyJSON(b []byte) string {
	if len(b) == 0 {
		return "{}"
	}
	return string(b)
}

func replanCoverageCampaigns(ctx context.Context, pool *pgxpool.Pool, sugar *zap.SugaredLogger) {
	rows, err := pool.Query(ctx, `
		SELECT id::text, COALESCE(pack_json, '{}'::jsonb), test_only, created_by::text
		FROM campaigns
		WHERE status IN ('active', 'paused')
		  AND COALESCE(pack_json->'coverage'->>'enabled', 'false') = 'true'
	`)
	if err != nil {
		sugar.Warnw("coverage replan query", "err", err)
		return
	}
	defer rows.Close()

	sites, err := loadOrchestratorCoverageSites(ctx, pool)
	if err != nil {
		sugar.Warnw("coverage replan sites", "err", err)
		return
	}
	coverage.EnrichWeather(sites)

	for rows.Next() {
		var id, owner string
		var packJSON []byte
		var testOnly bool
		if err := rows.Scan(&id, &packJSON, &testOnly, &owner); err != nil {
			continue
		}
		pack, err := campaign.ParsePack(packJSON)
		if err != nil || pack.Coverage == nil || !pack.Coverage.Enabled {
			continue
		}
		intent := coverage.Intent{
			Enabled:             pack.Coverage.Enabled,
			NMain:               pack.Coverage.NMain,
			Redundancy:          pack.Coverage.Redundancy,
			MaxGapMin:           pack.Coverage.MaxGapMin,
			MaxSites:            pack.Coverage.MaxSites,
			Mode:                pack.Coverage.Mode,
			PreferredSites:      append([]string{}, pack.Coverage.PreferredSites...),
			MinSites:            pack.Coverage.MinSites,
			MinLongitudeSpanDeg: pack.Coverage.MinLongitudeSpanDeg,
		}.Normalize()
		var ra, dec float64
		if len(pack.Targets) > 0 {
			ra, dec = pack.Targets[0].RA, pack.Targets[0].Dec
		}
		plan := coverage.GreedyFill(intent, sites, ra, dec, coverage.DefaultFactors(), testOnly || pack.TestOnly)
		if err := persistOrchestratorCoveragePlan(ctx, pool, id, packJSON, intent, plan); err != nil {
			sugar.Warnw("coverage replan persist", "campaign_id", id, "err", err)
			continue
		}
		// Keep handoff windows fresh for continuous urgency (skip hard failed plans).
		if !(intent.IsHard() && plan.GateStatus == "failed") {
			end := time.Now().UTC().Add(coverageSessionHours * time.Hour)
			_, _ = pool.Exec(ctx, `
			UPDATE tasks
			SET scheduled_end_at = $1, handoff_lead_seconds = $2, updated_at = NOW()
			WHERE campaign_id = $3::uuid
			  AND status NOT IN ('completed', 'superseded')
		`, end, coverageHandoffLeadSec, id)
		}

		if owner != "" {
			evtType := "coverage.replanned"
			msg := "Coverage plan refreshed"
			if plan.GateStatus == "degraded" {
				evtType = "coverage.degraded"
				msg = "Coverage plan degraded"
			} else if plan.GateStatus == "failed" {
				evtType = "coverage.failed"
				msg = "Coverage plan failed gates"
			}
			payload, _ := json.Marshal(map[string]any{
				"primary":      coverage.SiteIDs(plan.Primary),
				"redundant":    coverage.SiteIDs(plan.Redundant),
				"coverage_h":   plan.CoverageH,
				"max_gap_min":  plan.MaxGapMin,
				"gate_status":  plan.GateStatus,
				"gate_reasons": plan.GateReasons,
				"lon_span_deg": plan.LongitudeSpanDeg,
			})
			_, _ = pool.Exec(ctx, `
				INSERT INTO researcher_events (user_id, kind, event_type, message, campaign_id, payload)
				VALUES ($1::uuid, 'update', $4, $5, $2::uuid, $3::jsonb)
			`, owner, id, string(payload), evtType, msg)
		}
		sugar.Infow("coverage replanned",
			"campaign_id", id,
			"primary", coverage.SiteIDs(plan.Primary),
			"redundant", coverage.SiteIDs(plan.Redundant),
			"gate_status", plan.GateStatus,
		)
	}
}

func loadOrchestratorCoverageSites(ctx context.Context, pool *pgxpool.Pool) ([]coverage.Site, error) {
	rows, err := pool.Query(ctx, `
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
	return sites, nil
}

func persistOrchestratorCoveragePlan(
	ctx context.Context,
	pool *pgxpool.Pool,
	campaignID string,
	packJSON []byte,
	intent coverage.Intent,
	plan coverage.Plan,
) error {
	var pack map[string]any
	if len(packJSON) > 0 {
		_ = json.Unmarshal(packJSON, &pack)
	}
	if pack == nil {
		pack = map[string]any{}
	}
	pack["coverage"] = intent
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
	raw, err := json.Marshal(pack)
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, `UPDATE campaigns SET pack_json = $1::jsonb WHERE id = $2::uuid`, string(raw), campaignID)
	return err
}
