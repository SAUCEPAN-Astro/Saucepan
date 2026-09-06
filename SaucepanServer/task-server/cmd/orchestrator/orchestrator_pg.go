package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"
	pgx "github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/saucepan/hotpath/shared"
	"github.com/saucepan/hotpath/shared/campaign"
	"go.uber.org/zap"
)

func connectPostgres(ctx context.Context, pgDSN string, sugar *zap.SugaredLogger) (*pgxpool.Pool, error) {
	var err error
	var pool *pgxpool.Pool
	for i := 0; i < 30; i++ {
		pool, err = pgxpool.New(ctx, pgDSN)
		if err != nil {
			sugar.Warnw("pgxpool attempt failed, retrying...", "attempt", i+1, "err", err)
			time.Sleep(1 * time.Second)
			continue
		}
		if pingErr := pool.Ping(ctx); pingErr != nil {
			sugar.Warnw("pg ping failed, retrying...", "attempt", i+1, "err", pingErr)
			pool.Close()
			time.Sleep(1 * time.Second)
			continue
		}
		return pool, nil
	}
	return nil, fmt.Errorf("postgres connection failed after 30 retries: %w", err)
}

func setupPGListen(ctx context.Context, pool *pgxpool.Pool, sugar *zap.SugaredLogger) (*pgxpool.Conn, *pgx.Conn, error) {
	var listenPoolConn *pgxpool.Conn
	var pgListenConn *pgx.Conn
	var err error
	for i := 0; i < 30; i++ {
		listenPoolConn, err = pool.Acquire(ctx)
		if err != nil {
			sugar.Warnw("pg acquire failed, retrying...", "attempt", i+1, "err", err)
			time.Sleep(1 * time.Second)
			continue
		}
		pgListenConn = listenPoolConn.Conn()
		_, err = pgListenConn.Exec(ctx, "LISTEN new_task_channel")
		if err != nil {
			sugar.Warnw("pg listen failed, retrying...", "attempt", i+1, "err", err)
			listenPoolConn.Release()
			time.Sleep(1 * time.Second)
			continue
		}
		sugar.Info("listening on new_task_channel")
		return listenPoolConn, pgListenConn, nil
	}
	return nil, nil, fmt.Errorf("pg listen failed after 30 retries: %w", err)
}

func recoverPendingTasks(ctx context.Context, pool *pgxpool.Pool, rdb *redis.Client, sugar *zap.SugaredLogger) int {
	rows, err := pool.Query(ctx, `
		SELECT id, priority FROM tasks
		WHERE status = 'pending' AND assigned_telescope_id IS NULL
		ORDER BY priority ASC
	`)
	if err != nil {
		sugar.Warnw("recover tasks query", "err", err)
		return 0
	}
	defer rows.Close()
	pipe := rdb.Pipeline()
	count := 0
	for rows.Next() {
		var id, pri int
		if err := rows.Scan(&id, &pri); err != nil {
			continue
		}
		pipe.ZAdd(ctx, shared.RedisQueuedTasks, &redis.Z{Score: float64(pri), Member: float64(id)})
		count++
	}
	_, err = pipe.Exec(ctx)
	if err != nil {
		sugar.Warnw("recover tasks redis", "err", err)
	}
	if count > 0 {
		sugar.Infow("recovered pending tasks into Redis", "count", count)
	}
	return count
}

func warmCache(ctx context.Context, rdb *redis.Client, pool *pgxpool.Pool, sugar *zap.SugaredLogger) {
	rows, err := pool.Query(ctx, `
		SELECT telescope_id, power, limiting_magnitude, available_filters, is_emulator,
			COALESCE(reputation_stats->>'reliability_score', '0.8')::float AS reliability,
			aperture_mm, focal_length_mm, pixel_size_um,
			site_latitude, site_longitude,
			fov_width_arcmin, fov_height_arcmin, mount_type,
			median_seeing_arcsec, max_stable_exposure_s,
			obstruction_mask, mount_limits, horizon_profile,
			enabled_campaign_ids
		FROM telescopes WHERE is_active = true
	`)
	if err != nil {
		sugar.Warnw("warm cache query", "err", err)
		return
	}
	defer rows.Close()

	pipe := rdb.Pipeline()
	for rows.Next() {
		var (
			nodeID      string
			power       float64
			limitingMag *float64
			filters     []byte
			isEmulator  bool
			reliability float64
			aperture    *float64
			focal       *float64
			pixelSize   *float64
			siteLat     *float64
			siteLon     *float64
			fovW        *float64
			fovH        *float64
			mountType   *int
			seeing      *float64
			maxExp      *float64
			obstruction []byte
			mountLimits []byte
			horizon     []byte
			enabledIDs  []string
		)
		if err := rows.Scan(&nodeID, &power, &limitingMag, &filters, &isEmulator, &reliability,
			&aperture, &focal, &pixelSize, &siteLat, &siteLon,
			&fovW, &fovH, &mountType, &seeing, &maxExp,
			&obstruction, &mountLimits, &horizon, &enabledIDs); err != nil {
			continue
		}
		metaKey := fmt.Sprintf(shared.RedisNodeMeta, nodeID)
		pipe.HSet(ctx, metaKey, "node_id", nodeID)
		pipe.HSet(ctx, metaKey, "power", power)
		if limitingMag != nil {
			pipe.HSet(ctx, metaKey, "limiting_magnitude", *limitingMag)
		}
		pipe.HSet(ctx, metaKey, "is_emulator", isEmulator)
		pipe.HSet(ctx, metaKey, "reliability_score", reliability)
		pipe.HSet(ctx, metaKey, "quality_tier", "standard")
		if aperture != nil {
			pipe.HSet(ctx, metaKey, "aperture_mm", *aperture)
		}
		if focal != nil {
			pipe.HSet(ctx, metaKey, "focal_length_mm", *focal)
		}
		if pixelSize != nil {
			pipe.HSet(ctx, metaKey, "pixel_size_um", *pixelSize)
		}
		if siteLat != nil {
			pipe.HSet(ctx, metaKey, "site_lat", *siteLat)
		}
		if siteLon != nil {
			pipe.HSet(ctx, metaKey, "site_lon", *siteLon)
		}
		if fovW != nil {
			pipe.HSet(ctx, metaKey, "fov_width_arcmin", *fovW)
		}
		if fovH != nil {
			pipe.HSet(ctx, metaKey, "fov_height_arcmin", *fovH)
		}
		if mountType != nil {
			pipe.HSet(ctx, metaKey, "mount_type", *mountType)
		}
		if seeing != nil {
			pipe.HSet(ctx, metaKey, "median_seeing_arcsec", *seeing)
		}
		if maxExp != nil {
			pipe.HSet(ctx, metaKey, "max_stable_exposure_s", *maxExp)
		}
		if len(filters) > 0 {
			pipe.HSet(ctx, metaKey, "available_filters", string(filters))
		}
		if len(obstruction) > 0 {
			pipe.HSet(ctx, metaKey, "obstruction_mask", string(obstruction))
		}
		if len(mountLimits) > 0 {
			pipe.HSet(ctx, metaKey, "mount_limits", string(mountLimits))
		}
		if len(horizon) > 0 {
			pipe.HSet(ctx, metaKey, "horizon_profile", string(horizon))
		}
		if len(enabledIDs) > 0 {
			idsJSON, _ := json.Marshal(enabledIDs)
			pipe.HSet(ctx, metaKey, "enabled_campaign_ids", string(idsJSON))
		}
	}
	_, err = pipe.Exec(ctx)
	if err != nil {
		sugar.Warnw("warm cache redis pipeline", "err", err)
	}
	sugar.Info("cache warmed from postgres")
}

// fetchTaskNotifyPayload loads pending+unassigned task fields needed for MQTT assignment.
// When requeue is true the task is pending but blocked (e.g. paused campaign) and should stay in Redis.
func fetchTaskNotifyPayload(ctx context.Context, pool *pgxpool.Pool, taskID int) (*shared.NotifyPayload, bool, error) {
	var (
		p                shared.NotifyPayload
		allowEmulator    bool
		integrationTime  *float64
		minPower         *float64
		targetRA         *float64
		targetDec        *float64
		minAltitudeDeg   *float64
		filters          []string
		status           string
		assignedTel      *string
		campaignStatus   *string
		campaignID       *string
		schedEnd         *time.Time
		userEnd          *time.Time
		leadSeconds      *int
		emergencyAt      *time.Time
		packJSON         []byte
		pierCodeDisabled bool
		failureCount     int
		lastAttemptAt    *time.Time
	)
	err := pool.QueryRow(ctx, `
		SELECT t.id, t.name, t.priority, t.status, t.assigned_telescope_id,
		       t.integration_time, t.min_power, t.required_filters,
		       t.target_ra, t.target_dec, t.min_altitude_deg, t.allow_emulator,
		       t.min_aperture_mm, t.min_sub_exposure_s, t.min_resolution_arcsec, t.max_resolution_arcsec,
		       t.min_psf_fwhm_arcsec, t.max_psf_fwhm_arcsec,
		       t.required_fov_width_arcmin, t.required_fov_height_arcmin, COALESCE(t.science_band, ''),
		       c.status, t.campaign_id::text,
		       t.scheduled_end_at, t.user_end_at, t.handoff_lead_seconds, t.emergency_handoff_requested_at,
		       COALESCE(c.pack_json, '{}'::jsonb),
		       COALESCE(c.pier_code_disabled, false),
		       COALESCE(t.failure_count, 0), t.last_assignment_attempt_at
		FROM tasks t
		LEFT JOIN campaigns c ON t.campaign_id = c.id
		WHERE t.id = $1
	`, taskID).Scan(
		&p.TaskID, &p.Name, &p.Priority, &status, &assignedTel,
		&integrationTime, &minPower, &filters,
		&targetRA, &targetDec, &minAltitudeDeg, &allowEmulator,
		&p.MinApertureMM, &p.MinSubExposureS, &p.MinResolutionArcsec, &p.MaxResolutionArcsec,
		&p.MinPSFFWHMArcsec, &p.MaxPSFFWHMArcsec,
		&p.RequiredFOVWidthArcmin, &p.RequiredFOVHeightArcmin, &p.ScienceBand,
		&campaignStatus, &campaignID,
		&schedEnd, &userEnd, &leadSeconds, &emergencyAt,
		&packJSON, &pierCodeDisabled,
		&failureCount, &lastAttemptAt,
	)
	if err != nil {
		return nil, false, err
	}
	if !shared.TaskAssignable(status, assignedTel) {
		return nil, false, nil
	}
	// Reclaim backoff (#403): a task that recently lost its lease sits out an
	// exponential window (min(BASE * 2^failure_count, 30m)) before it may be
	// re-selected. Return requeue=true so it stays parked in Redis, not dropped.
	if lastAttemptAt != nil &&
		taskInBackoff(*lastAttemptAt, leaseCfg.BackoffBase, failureCount, reclaimNow()) {
		return nil, true, nil
	}
	cs := ""
	if campaignStatus != nil {
		cs = *campaignStatus
	}
	if !campaign.AllowsAssign(cs) {
		return nil, true, nil
	}
	p.IntegrationTime = integrationTime
	p.MinPower = minPower
	p.RequiredFilters = filters
	p.TargetRA = targetRA
	p.TargetDec = targetDec
	p.MinAltitudeDeg = minAltitudeDeg
	p.AllowEmulator = allowEmulator
	if campaignID != nil {
		p.CampaignID = *campaignID
	}
	p.ScheduledEndAt = schedEnd
	p.UserEndAt = userEnd
	p.HandoffLeadSeconds = leadSeconds
	p.EmergencyHandoffRequestedAt = emergencyAt
	applyCoverageFromPack(&p, packJSON)
	applySeasonFromPack(&p, packJSON)
	applyPierCodeFromPack(&p, packJSON)
	// Kill switch (#470 step 7 / #520) — a server-set campaign flag, read on
	// every assign. When set, grants + artifact still ride the payload but
	// PierCodeDisabled tells the pier to skip running or continuing this
	// campaign's code at its next check-in.
	p.PierCodeDisabled = pierCodeDisabled
	return &p, false, nil
}

// applyPierCodeFromPack hydrates NotifyPayload.PierCodeGrants and .PierCode
// from the campaign pack's `pier_code` block (#470 step 3 / #516, step 5 /
// #518). A pack that does not enable pier_code leaves both nil and nothing is
// sent on the assign. The kill switch (campaigns.pier_code_disabled) is read
// separately (#520).
func applyPierCodeFromPack(p *shared.NotifyPayload, packJSON []byte) {
	if p == nil || len(packJSON) == 0 {
		return
	}
	pack, err := campaign.ParsePack(packJSON)
	if err != nil {
		return
	}
	p.PierCodeGrants = campaign.EffectivePierCodeGrants(pack)
	p.PierCode = campaign.EffectivePierCodeArtifact(pack)
}

func applyCoverageFromPack(p *shared.NotifyPayload, packJSON []byte) {
	if p == nil || len(packJSON) == 0 {
		return
	}
	var pack struct {
		Coverage *struct {
			Enabled bool   `json:"enabled"`
			Mode    string `json:"mode"`
		} `json:"coverage"`
		CoveragePlan *struct {
			Primary   []string `json:"primary"`
			Redundant []string `json:"redundant"`
		} `json:"coverage_plan"`
	}
	if err := json.Unmarshal(packJSON, &pack); err != nil {
		return
	}
	if pack.Coverage == nil || !pack.Coverage.Enabled {
		return
	}
	p.CoverageEnabled = true
	p.CoverageHardMode = strings.EqualFold(strings.TrimSpace(pack.Coverage.Mode), "hard")
	if pack.CoveragePlan != nil {
		p.CoveragePrimary = append([]string{}, pack.CoveragePlan.Primary...)
		p.CoverageRedundant = append([]string{}, pack.CoveragePlan.Redundant...)
	}
}
