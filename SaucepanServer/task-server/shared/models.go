package shared

import (
	"go.uber.org/zap"
	"time"
)

// ═══════════════════════════════════════════════════════════════
// Timing instrumentation — logs every step with microseconds
// ═══════════════════════════════════════════════════════════════

type Timer struct {
	Start time.Time
	Label string
	Steps []TimerStep
}

type TimerStep struct {
	Name     string
	Elapsed  time.Duration // since Timer.Start
	Duration time.Duration // since last step
	At       time.Time
}

func NewTimer(label string) *Timer {
	now := time.Now()
	return &Timer{Start: now, Label: label, Steps: []TimerStep{}}
}

func (t *Timer) Step(name string) {
	now := time.Now()
	prev := t.Start
	if len(t.Steps) > 0 {
		prev = t.Steps[len(t.Steps)-1].At
	}
	t.Steps = append(t.Steps, TimerStep{
		Name:     name,
		Elapsed:  now.Sub(t.Start),
		Duration: now.Sub(prev),
		At:       now,
	})
}

func (t *Timer) Report(sugar *zap.SugaredLogger, taskID int, nodeID string) {
	totalUs, _, _ := t.SnapshotUs()
	sugar.Infow("TIMING_END "+t.Label,
		"task_id", taskID,
		"node_id", nodeID,
		"total_us", totalUs,
		"total_ms", totalUs/1000,
	)
	for _, s := range t.Steps {
		sugar.Infow("TIMING "+t.Label+"."+s.Name,
			"task_id", taskID,
			"node_id", nodeID,
			"offset_us", s.Elapsed.Microseconds(),
			"delta_us", s.Duration.Microseconds(),
		)
	}
}

// SnapshotUs returns (total elapsed since Start, offset of the last step since
// Start, duration of the last step since the previous step) in microseconds.
// Used to populate ops.timer_total_us / ops.timer_step_offset_us /
// ops.timer_step_delta_us on hot-path Observations (#370).
func (t *Timer) SnapshotUs() (totalUs, offsetUs, deltaUs int64) {
	totalUs = time.Since(t.Start).Microseconds()
	if len(t.Steps) == 0 {
		return totalUs, 0, 0
	}
	last := t.Steps[len(t.Steps)-1]
	return totalUs, last.Elapsed.Microseconds(), last.Duration.Microseconds()
}

// ═══════════════════════════════════════════════════════════════
// Data models
// ═══════════════════════════════════════════════════════════════
//
// Telemetry, NodeMetadata, Command, AssignTaskPayload, and PreemptTaskPayload
// moved to shared/wire/models.go (#457) — the pier-facing wire contract now
// lives in a stdlib-only package so cmd/saucepan can depend on it without
// pulling in zap/redis. shared/wire_alias.go re-exports them here so no
// existing call site changes.

// CohortMaxNodes — max telescopes per stacking cohort (orchestrator fan-out cap).
const CohortMaxNodes = 8

// NodeState — what the orchestrator tracks in Redis for each node
type NodeState struct {
	NodeID              string   `json:"node_id"`
	Status              string   `json:"status"` // idle, busy, offline
	CurrentTaskID       *int     `json:"current_task_id,omitempty"`
	CurrentTaskPriority *int     `json:"current_task_priority,omitempty"`
	LoadPct             float64  `json:"load_pct"`
	EstimatedStartupMS  int      `json:"estimated_startup_ms"` // pre-computed
	QualityTier         string   `json:"quality_tier"`
	ReliabilityScore    float64  `json:"reliability_score"`
	MountAltDeg         *float64 `json:"mount_alt_deg,omitempty"`
	MountAzDeg          *float64 `json:"mount_az_deg,omitempty"`
	LastSeen            string   `json:"last_seen"` // ISO 8601
}

// Topic constants
//
// TopicTelemetry, TopicCommands, TopicMetadata, TopicStatus, and the four
// NodeStatus* values moved to shared/wire/topics.go (#457) and are
// re-declared (not aliased — Go has no const alias) in shared/wire_alias.go.
const (
	RedisNodeState = "node_state:%s"
	RedisNodeMeta  = "node_meta:%s"
	// RedisQueuedTasks — interrupt-lane ZSET awaiting hot-path assignment
	// (score=priority, member=task_id). Alias of lanes.RedisQueuedInterrupt (#421).
	// Drain / NOTIFY enqueue interrupt here; never leave assigned tasks in this set (#400).
	RedisQueuedTasks = "tasks:queued"
	// RedisQueuedPlanned — planned-lane ZSET; periodic planner materializes agendas (#421).
	RedisQueuedPlanned = "tasks:queued:planned"
	// RedisInflightTasks — ZSET of assigned tasks (tracked, not re-queued).
	RedisInflightTasks = "tasks:inflight"
	// RedisActiveTasks is the legacy assign ZSET name. Startup still DELs it;
	// hot path uses RedisQueuedTasks / RedisInflightTasks only (#400).
	RedisActiveTasks = "tasks:active"
	RedisActiveNodes = "nodes:active" // set: active node IDs (managed by collector)
	RedisTaskChannel = "tasks:new"    // Pub/Sub channel — fast signal for new tasks
	RedisNodeOffline = "node_offline:%s"

	// StateTTLSeconds — Redis node_state / active-nodes TTL. Raised to 180s (#370)
	// to comfortably span the 60s on-change/heartbeat telemetry cadence without
	// nodes flapping offline between publishes.
	StateTTLSeconds = 180

	// PreemptThresholdDefault — minimum priority difference to justify preemption.
	// Lower priority number = higher urgency. Preempt only if new priority is at least
	// this much better (lower) than the current task. Default 10.
	// Set via PREEMPT_PRIORITY_THRESHOLD env var.
	PreemptThresholdDefault = 10

	// SlewNearbyThresholdMsDefault — if slew time to reach target is below this,
	// the node is considered "nearby" and preemption is allowed even without a
	// significant priority difference. Set via SLEW_NEARBY_THRESHOLD_MS env var.
	SlewNearbyThresholdMsDefault = 5000 // 5 seconds
)

// NotifyPayload — what Postgres sends via LISTEN/NOTIFY
type NotifyPayload struct {
	TaskID                  int      `json:"task_id"`
	Priority                int      `json:"priority"`
	Name                    string   `json:"name,omitempty"`
	MinPower                *float64 `json:"min_power,omitempty"`
	TargetMagnitude         *float64 `json:"target_magnitude,omitempty"`
	RequiredFilters         []string `json:"required_filters,omitempty"`
	IntegrationTime         *float64 `json:"integration_time,omitempty"`
	TargetRA                *float64 `json:"target_ra,omitempty"`
	TargetDec               *float64 `json:"target_dec,omitempty"`
	MinAltitudeDeg          *float64 `json:"min_altitude_deg,omitempty"`
	AllowEmulator           bool     `json:"allow_emulator,omitempty"`
	MinApertureMM           *float64 `json:"min_aperture_mm,omitempty"`
	MinSubExposureS         *float64 `json:"min_sub_exposure_s,omitempty"`
	MinResolutionArcsec     *float64 `json:"min_resolution_arcsec,omitempty"`
	MaxResolutionArcsec     *float64 `json:"max_resolution_arcsec,omitempty"`
	MinPSFFWHMArcsec        *float64 `json:"min_psf_fwhm_arcsec,omitempty"`
	MaxPSFFWHMArcsec        *float64 `json:"max_psf_fwhm_arcsec,omitempty"`
	RequiredFOVWidthArcmin  *float64 `json:"required_fov_width_arcmin,omitempty"`
	RequiredFOVHeightArcmin *float64 `json:"required_fov_height_arcmin,omitempty"`
	ScienceBand             string   `json:"science_band,omitempty"`
	CampaignID              string   `json:"campaign_id,omitempty"`
	CreatedAt               string   `json:"created_at,omitempty"` // ISO 8601 from PG
	// Handoff fields for urgency-boosted assignment (#86).
	ScheduledEndAt              *time.Time `json:"-"`
	UserEndAt                   *time.Time `json:"-"`
	HandoffLeadSeconds          *int       `json:"-"`
	EmergencyHandoffRequestedAt *time.Time `json:"-"`
	// Coverage fields for 24/7 relay (#84) — server-authoritative preferred sites.
	CoverageEnabled   bool     `json:"-"`
	CoveragePrimary   []string `json:"-"`
	CoverageRedundant []string `json:"-"`
	CoverageHardMode  bool     `json:"-"` // #397: no preferred fail-open
	// Season fields from campaigns.pack_json for planned vs interrupt (#421).
	SeasonKind           string  `json:"-"`
	SeasonUrgency        string  `json:"-"`
	SeasonCadenceGoalMin int     `json:"-"`
	SeasonWindowStart    *string `json:"-"`
	SeasonWindowEnd      *string `json:"-"`
	// On-pier researcher code (#470 step 3 / #516), hydrated from
	// campaigns.pack_json `pier_code` and campaigns.pier_code_disabled.
	PierCodeGrants   map[string]bool `json:"-"`
	PierCodeDisabled bool            `json:"-"`
	// PierCode is the researcher artifact reference (#470 step 5 / #518) —
	// content hash + fetch URL — hydrated from campaigns.pack_json
	// `pier_code.artifact`. Nil unless the pack enables pier_code and names an
	// artifact; the assign carries it through to the pier verbatim.
	PierCode *PierCodeRef `json:"-"`
}
