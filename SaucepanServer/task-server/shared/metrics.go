package shared

import (
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
	"go.uber.org/zap"

	hpmetrics "github.com/saucepan/hotpath/internal/metrics"
)

// ═══════════════════════════════════════════════════════════════
// Metrics — structured event logging for the hot path.
// Every assignment, preemption, and queue decision produces a
// JSON event sent to Redis stream "metrics:events" and also
// logged via zap for real-time tails. Optionally publishes an
// Observation envelope to MQTT saucepan/nodes/{id}/metrics (#24).
// #370 adds hotpathGauges (point-in-time queue/node/stream state,
// refreshed on Flush) and latency/timer/service fields on TaskEvent
// so all 11 formerly-wait Go hot-path metrics are actually produced.
// ═══════════════════════════════════════════════════════════════

const metricsEventsStream = "metrics:events"
const metricsCountersKey = "metrics:counters"

type TaskEventType string

const (
	EventTaskAssigned  TaskEventType = "task_assigned"
	EventTaskPreempted TaskEventType = "task_preempted"
	EventTaskQueued    TaskEventType = "task_queued"
)

// TaskEvent is emitted for every orchestrator decision.
type TaskEvent struct {
	Event     TaskEventType `json:"event"`
	Timestamp time.Time     `json:"ts"`
	TaskID    int           `json:"task_id"`
	Priority  int           `json:"priority"`

	// Assignment fields
	NodeID                string  `json:"node_id,omitempty"`
	SlewTimeMs            int     `json:"slew_ms,omitempty"`
	IsPreemption          bool    `json:"is_preemption,omitempty"`
	PrevTaskID            *int    `json:"prev_task_id,omitempty"`
	NodeQualityTier       string  `json:"node_tier,omitempty"`
	NodeSlewRateDegS      float64 `json:"node_slew_rate,omitempty"`
	SelectorReason        string  `json:"selector_reason,omitempty"`
	OrchestratorLatencyUs int64   `json:"orchestrator_latency_us,omitempty"`
	IntegrationTimeSec    float64 `json:"integration_time_sec,omitempty"`

	// Queue fields
	QueueReason string `json:"queue_reason,omitempty"`

	// Preemption fields
	PriorityDiff int `json:"priority_diff,omitempty"`

	// Filter / obstruction info
	RequiredFilters         []string `json:"required_filters,omitempty"`
	NodeFilters             []string `json:"node_filters,omitempty"`
	TargetBelowHorizon      bool     `json:"target_below_horizon,omitempty"`
	TargetBehindObstruction bool     `json:"target_behind_obstruction,omitempty"`

	// Hot-path timing / service-health fields (#370, decisions.yaml §11 & §13).
	PGNotifyLatencyMS    *float64 `json:"pg_notify_latency_ms,omitempty"`
	RedisPubsubLatencyMS *float64 `json:"redis_pubsub_latency_ms,omitempty"`
	MQTTPublishLatencyMS *float64 `json:"mqtt_publish_latency_ms,omitempty"`
	TimerTotalUs         *int64   `json:"timer_total_us,omitempty"`
	TimerStepOffsetUs    *int64   `json:"timer_step_offset_us,omitempty"`
	TimerStepDeltaUs     *int64   `json:"timer_step_delta_us,omitempty"`
	CampaignID           string   `json:"campaign_id,omitempty"`
	HandoffRequested     bool     `json:"handoff_requested,omitempty"`
}

// RecordTimer copies a Timer's total/offset/delta snapshot (microseconds) onto
// a TaskEvent so it rides along on the next Observation (#370).
func RecordTimer(evt *TaskEvent, t *Timer) {
	if t == nil {
		return
	}
	total, offset, delta := t.SnapshotUs()
	evt.TimerTotalUs = &total
	evt.TimerStepOffsetUs = &offset
	evt.TimerStepDeltaUs = &delta
}

// hotpathGauges is a point-in-time snapshot of queue/node/stream state,
// refreshed once per Flush() rather than on every Emit() to keep the hot
// path allocation-free. EventCounters mirrors "metrics:counters" (Redis).
type hotpathGauges struct {
	QueueDepth     int64
	NodesActive    int64
	StaleTaskCount int64
	StreamLen      int64
	EventCounters  map[string]int64

	// Service-health leftovers (decisions.yaml §13), tracked in-process
	// because there is no dedicated task-failure event stream yet.
	TaskFailureCount  int64
	TaskAccExptimeS   float64
	LastAssignAttempt time.Time

	UpdatedAt time.Time
}

// MetricsCollector aggregates events and flushes to Redis + zap.
type MetricsCollector struct {
	mu       sync.Mutex
	events   []TaskEvent
	rdb      *redis.Client
	sugar    *zap.SugaredLogger
	flushInt time.Duration
	stopCh   chan struct{}
	pub      hpmetrics.Publisher
	gauges   hotpathGauges

	taskFailureCount int64
	taskAccExptimeS  float64
	lastAssignAt     time.Time
}

func NewMetricsCollector(rdb *redis.Client, sugar *zap.SugaredLogger, flushInterval time.Duration) *MetricsCollector {
	m := &MetricsCollector{
		events:   make([]TaskEvent, 0, 1024),
		rdb:      rdb,
		sugar:    sugar,
		flushInt: flushInterval,
		stopCh:   make(chan struct{}),
	}
	m.refreshGauges()
	if flushInterval > 0 {
		go m.flushLoop()
	}
	return m
}

// SetMQTTPublisher enables Observation publish on Emit (fail-open).
func (m *MetricsCollector) SetMQTTPublisher(pub hpmetrics.Publisher) {
	m.mu.Lock()
	m.pub = pub
	m.mu.Unlock()
}

func (m *MetricsCollector) Stop() {
	close(m.stopCh)
	m.Flush()
}

func (m *MetricsCollector) Emit(evt TaskEvent) {
	// Always log to zap in structured JSON format
	m.sugar.Infow("METRICS_"+string(evt.Event),
		"event", evt.Event,
		"task_id", evt.TaskID,
		"priority", evt.Priority,
		"node_id", evt.NodeID,
		"slew_ms", evt.SlewTimeMs,
		"is_preemption", evt.IsPreemption,
		"prev_task_id", evt.PrevTaskID,
		"node_tier", evt.NodeQualityTier,
		"node_slew_rate", evt.NodeSlewRateDegS,
		"selector_reason", evt.SelectorReason,
		"orchestrator_latency_us", evt.OrchestratorLatencyUs,
		"queue_reason", evt.QueueReason,
		"priority_diff", evt.PriorityDiff,
		"campaign_id", evt.CampaignID,
		"handoff_requested", evt.HandoffRequested,
	)

	// Buffer for Redis flush; update in-process service-health counters.
	m.mu.Lock()
	m.events = append(m.events, evt)
	if evt.Event == EventTaskQueued && evt.QueueReason == "no_visible_node" {
		// Proxy for ops.task_failure_count: no task-failure event stream exists
		// yet, so an assignment that found zero eligible nodes counts as one.
		m.taskFailureCount++
	}
	if evt.Event == EventTaskAssigned || evt.Event == EventTaskPreempted {
		m.lastAssignAt = evt.Timestamp
		m.taskAccExptimeS += evt.IntegrationTimeSec
	}
	pub := m.pub
	gauges := m.gauges
	m.mu.Unlock()

	// MQTT Observation — never blocks task push (#24, SLO=notify only)
	if pub != nil && evt.NodeID != "" {
		obs := evt.ToObservation(gauges)
		if err := hpmetrics.PublishObservation(pub, evt.NodeID, obs); err != nil {
			m.sugar.Warnw("metrics mqtt publish failed", "err", err, "node_id", evt.NodeID)
		}
	}
}

// ToObservation maps a TaskEvent + the latest hotpathGauges snapshot into the
// metrics Observation envelope (#370 — all 11 formerly-wait Go hot-path IDs).
func (evt TaskEvent) ToObservation(gauges hotpathGauges) hpmetrics.Observation {
	metrics := map[string]interface{}{
		"ops.evt_type":        string(evt.Event),
		"ops.evt_ts":          evt.Timestamp.UTC().Format(time.RFC3339Nano),
		"ops.task_id":         evt.TaskID,
		"ops.priority":        evt.Priority,
		"ops.node_id":         evt.NodeID,
		"ops.slew_ms":         evt.SlewTimeMs,
		"ops.is_preemption":   evt.IsPreemption,
		"ops.orch_latency_us": evt.OrchestratorLatencyUs,
	}
	if evt.PrevTaskID != nil {
		metrics["ops.prev_task_id"] = *evt.PrevTaskID
	}
	if evt.PriorityDiff != 0 {
		metrics["ops.priority_diff"] = evt.PriorityDiff
	}
	if evt.NodeQualityTier != "" {
		metrics["ops.node_tier"] = evt.NodeQualityTier
	}
	if evt.NodeSlewRateDegS != 0 {
		metrics["ops.node_slew_rate"] = evt.NodeSlewRateDegS
	}
	if evt.SelectorReason != "" {
		metrics["ops.selector_reason"] = evt.SelectorReason
	}
	if evt.QueueReason != "" {
		metrics["ops.queue_reason"] = evt.QueueReason
	}
	if evt.TargetBelowHorizon {
		metrics["ops.target_below_horizon"] = true
	}
	if evt.TargetBehindObstruction {
		metrics["ops.target_obstructed"] = true
	}
	if evt.CampaignID != "" {
		metrics["ops.campaign_id"] = evt.CampaignID
	}
	if evt.HandoffRequested {
		metrics["ops.handoff_requested"] = true
	}

	// Hot-path timing (decisions.yaml §11).
	if evt.PGNotifyLatencyMS != nil {
		metrics["ops.pg_notify_latency_ms"] = *evt.PGNotifyLatencyMS
	}
	if evt.RedisPubsubLatencyMS != nil {
		metrics["ops.redis_pubsub_latency_ms"] = *evt.RedisPubsubLatencyMS
	}
	if evt.MQTTPublishLatencyMS != nil {
		metrics["ops.mqtt_publish_latency_ms"] = *evt.MQTTPublishLatencyMS
	}
	if evt.TimerTotalUs != nil {
		metrics["ops.timer_total_us"] = *evt.TimerTotalUs
	}
	if evt.TimerStepOffsetUs != nil {
		metrics["ops.timer_step_offset_us"] = *evt.TimerStepOffsetUs
	}
	if evt.TimerStepDeltaUs != nil {
		metrics["ops.timer_step_delta_us"] = *evt.TimerStepDeltaUs
	}

	// Hot-path gauges, sampled once per Flush interval (#370).
	metrics["ops.queue_depth"] = gauges.QueueDepth
	metrics["ops.nodes_active"] = gauges.NodesActive
	metrics["ops.stale_task_count"] = gauges.StaleTaskCount
	metrics["ops.metrics_stream_len"] = gauges.StreamLen
	if gauges.EventCounters != nil {
		metrics["ops.event_counters"] = gauges.EventCounters
		if v, ok := gauges.EventCounters["preemptions:total"]; ok {
			metrics["ops.preemptions_total"] = v
		}
	}

	// Service-health leftovers (decisions.yaml §13) — notify-only, SLOs in slo_config.yaml.
	metrics["ops.task_failure_count"] = gauges.TaskFailureCount
	metrics["ops.task_acc_exptime"] = gauges.TaskAccExptimeS
	if !gauges.LastAssignAttempt.IsZero() {
		metrics["ops.last_assign_attempt"] = gauges.LastAssignAttempt.UTC().Format(time.RFC3339)
	}
	// No standalone Prometheus exporter is deployed (post-alpha per wait_pile
	// notes); this flag documents that honestly rather than claiming one exists.
	metrics["ops.prometheus_export"] = false

	ctx := map[string]interface{}{
		"node_id": evt.NodeID,
		"task_id": evt.TaskID,
	}
	entityID := evt.NodeID
	if entityID == "" {
		entityID = fmt.Sprintf("task_%d", evt.TaskID)
	}
	return hpmetrics.NewObservation("hotpath_ops", "ops", entityID, metrics, ctx)
}

func (m *MetricsCollector) Flush() {
	m.mu.Lock()
	events := m.events
	m.events = make([]TaskEvent, 0, 1024)
	m.mu.Unlock()

	// Mirror refreshGauges()'s nil guard: with no Redis client the collector
	// still logs + buffers in-process (Emit), but has nowhere to flush to.
	// The buffer was already drained above, so the events are simply dropped.
	if m.rdb != nil && len(events) > 0 {
		ctx := m.rdb.Context()
		pipe := m.rdb.Pipeline()
		for _, evt := range events {
			data, err := json.Marshal(evt)
			if err != nil {
				continue
			}
			pipe.XAdd(ctx, &redis.XAddArgs{
				Stream: metricsEventsStream,
				Values: map[string]interface{}{
					"event": string(data),
				},
			})
			pipe.HIncrBy(ctx, metricsCountersKey, fmt.Sprintf("%s:total", evt.Event), 1)
			if evt.IsPreemption {
				pipe.HIncrBy(ctx, metricsCountersKey, "preemptions:total", 1)
			}
			if evt.NodeID != "" {
				pipe.HIncrBy(ctx, "metrics:node:"+evt.NodeID, "tasks_assigned", 1)
				if evt.IsPreemption {
					pipe.HIncrBy(ctx, "metrics:node:"+evt.NodeID, "tasks_preempted", 1)
				}
				pipe.HIncrBy(ctx, "metrics:node:"+evt.NodeID, "total_slew_ms", int64(evt.SlewTimeMs))
			}
		}
		pipe.XTrimMaxLen(ctx, metricsEventsStream, 100000)
		if _, err := pipe.Exec(ctx); err != nil {
			m.sugar.Warnw("metrics flush failed", "err", err)
		}
	}

	m.refreshGauges()
}

// refreshGauges samples Redis queue/node/stream state plus in-process
// service-health counters into a single snapshot used by ToObservation.
// Cheap and rate-limited to once per Flush — never called from the
// per-event hot path (#370).
func (m *MetricsCollector) refreshGauges() {
	g := hotpathGauges{UpdatedAt: time.Now().UTC()}
	if m.rdb != nil {
		ctx := m.rdb.Context()
		if n, err := m.rdb.ZCard(ctx, RedisQueuedTasks).Result(); err == nil {
			g.QueueDepth = n
		}
		if n, err := m.rdb.SCard(ctx, RedisActiveNodes).Result(); err == nil {
			g.NodesActive = n
		}
		// Inflight proxy: assigned tasks tracked outside the assign queue (#400).
		if n, err := m.rdb.ZCard(ctx, RedisInflightTasks).Result(); err == nil {
			g.StaleTaskCount = n
		}
		if n, err := m.rdb.XLen(ctx, metricsEventsStream).Result(); err == nil {
			g.StreamLen = n
		}
		if counters, err := m.rdb.HGetAll(ctx, metricsCountersKey).Result(); err == nil {
			ec := make(map[string]int64, len(counters))
			for k, v := range counters {
				if n, err := strconv.ParseInt(v, 10, 64); err == nil {
					ec[k] = n
				}
			}
			g.EventCounters = ec
		}
	}

	m.mu.Lock()
	g.TaskFailureCount = m.taskFailureCount
	g.TaskAccExptimeS = m.taskAccExptimeS
	g.LastAssignAttempt = m.lastAssignAt
	m.gauges = g
	m.mu.Unlock()
}

func (m *MetricsCollector) flushLoop() {
	ticker := time.NewTicker(m.flushInt)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			m.Flush()
		case <-m.stopCh:
			return
		}
	}
}
