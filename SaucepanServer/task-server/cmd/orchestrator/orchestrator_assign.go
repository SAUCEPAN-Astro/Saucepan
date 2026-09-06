package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/go-redis/redis/v8"
	pgxpool "github.com/jackc/pgx/v5/pgxpool"
	"github.com/saucepan/hotpath/shared"
	"github.com/saucepan/hotpath/shared/campaign"
	"github.com/saucepan/hotpath/shared/lanes"
	"go.uber.org/zap"
)

func handleTaskNotification(
	ctx context.Context,
	pool *pgxpool.Pool,
	payload shared.NotifyPayload,
	rdb *redis.Client,
	mqttClient mqtt.Client,
	metrics *shared.MetricsCollector,
	sugar *zap.SugaredLogger,
	preemptThreshold int,
	slewNearbyMs int,
) {
	t := shared.NewTimer("task_lifecycle")
	if payload.CreatedAt != "" {
		created, err := time.Parse(time.RFC3339Nano, payload.CreatedAt)
		if err == nil {
			t.Start = created
		}
	}
	t.Step("pg_notify_received")
	var pgNotifyLatencyMs float64
	if len(t.Steps) > 0 {
		pgNotifyLatencyMs = float64(t.Steps[0].Elapsed.Microseconds()) / 1000.0
	}

	loaded, requeue, err := fetchTaskNotifyPayload(ctx, pool, payload.TaskID)
	if err != nil {
		sugar.Warnw("task load for assign", "task_id", payload.TaskID, "err", err)
		return
	}
	if loaded == nil {
		// Already assigned/terminal — do not re-queue (#400).
		if requeue {
			enqueueByLane(ctx, rdb, payload.TaskID, payload.Priority, lanes.LanePlanned)
			sugar.Infow("task queued (campaign not active)", "task_id", payload.TaskID)
		}
		return
	}
	lane := lanes.ClassifyLane(seasonInputsFromPayload(*loaded))
	if lane == lanes.LanePlanned {
		// Planned work goes to the planner agenda, not surprise ms dispatch (#421).
		enqueueByLane(ctx, rdb, loaded.TaskID, loaded.Priority, lanes.LanePlanned)
		sugar.Infow("task enqueued planned lane",
			"task_id", loaded.TaskID,
			"season_kind", loaded.SeasonKind,
			"season_urgency", loaded.SeasonUrgency,
			"cadence_goal_min", loaded.SeasonCadenceGoalMin,
		)
		metrics.Emit(shared.TaskEvent{
			Event:       shared.EventTaskQueued,
			Timestamp:   time.Now().UTC(),
			TaskID:      loaded.TaskID,
			Priority:    loaded.Priority,
			QueueReason: "planned_lane",
			CampaignID:  loaded.CampaignID,
		})
		return
	}
	assignTask(ctx, pool, *loaded, rdb, mqttClient, metrics, sugar, preemptThreshold, slewNearbyMs, t, pgNotifyLatencyMs)
}

// assignTask is the shared NOTIFY + drain assign path (#401): select cohort,
// claim Postgres row (pending→assigned), MQTT publish, Redis busy + queued→inflight.
func assignTask(
	ctx context.Context,
	pool *pgxpool.Pool,
	payload shared.NotifyPayload,
	rdb *redis.Client,
	mqttClient mqtt.Client,
	metrics *shared.MetricsCollector,
	sugar *zap.SugaredLogger,
	preemptThreshold int,
	slewNearbyMs int,
	t *shared.Timer,
	pgNotifyLatencyMs float64,
) {
	if t == nil {
		t = shared.NewTimer("task_lifecycle")
	}

	now := time.Now().UTC()
	ht := shared.HandoffTaskFromNotify(payload)
	basePri := payload.Priority
	payload.Priority = shared.EffectiveAssignPriority(basePri, ht, now)
	if payload.Priority != basePri {
		sugar.Infow("handoff urgency boost",
			"task_id", payload.TaskID,
			"base_priority", basePri,
			"effective_priority", payload.Priority,
			"urgency", shared.UrgencyForTask(ht, now),
		)
	}

	sugar.Infow("new task",
		"task_id", payload.TaskID,
		"priority", payload.Priority,
		"ra", payload.TargetRA,
		"dec", payload.TargetDec,
		"filters", payload.RequiredFilters,
	)

	nodes, err := fetchAllNodeEvals(ctx, rdb, sugar)
	if err != nil {
		sugar.Warnw("fetch node evals", "err", err)
		enqueueByLane(ctx, rdb, payload.TaskID, payload.Priority, lanes.LaneInterrupt)
		return
	}
	nodes = filterNodesByCampaign(nodes, payload.CampaignID)
	attachPlanRemaining(ctx, rdb, nodes)
	req := shared.RequirementsFromNotify(payload)
	if len(req.PreferredNodeIDs) > 0 {
		before := len(nodes)
		nodes = shared.FilterPreferredNodesMode(nodes, req.PreferredNodeIDs, req.PreferredFailOpen)
		sugar.Infow("coverage preferred sites",
			"task_id", payload.TaskID,
			"preferred", req.PreferredNodeIDs,
			"fail_open", req.PreferredFailOpen,
			"nodes_before", before,
			"nodes_after", len(nodes),
		)
	}
	// Interrupt: prefer standby roster cache before scoring the full fleet (#421).
	seasonIn := seasonInputsFromPayload(payload)
	roster := loadStandbyRoster(ctx, rdb, lanes.AlertClass(seasonIn))
	selectPool := lanes.PreferRoster(nodes, roster)
	t.Step("fetch_node_evaluations")

	assignments := selectAssignments(
		nodes, selectPool, len(roster), req,
		payload.Priority, preemptThreshold, slewNearbyMs, now,
	)
	t.Step("select_eligible_nodes")

	exec := newPGRedisExec(pool, rdb, mqttClient, sugar)
	nodeIDs := assignOnce(ctx, exec, payload, assignments, nodes, metrics, sugar, t, now, pgNotifyLatencyMs)
	if len(nodeIDs) > 0 && payload.CoverageEnabled {
		rollCoverageSession(ctx, pool, payload.TaskID, sugar)
	}
}

// selectAssignments runs the selection ladder shared by the NOTIFY and drain
// entry points (#401): cohort fill on the roster-preferred pool, single-best
// preemption fallback, then a full-fleet retry when a non-empty standby roster
// narrowed the pool and nothing matched (#421 roster preference). Pure — no
// Redis/PG/MQTT, so the ladder is unit-testable on its own.
func selectAssignments(
	nodes, selectPool []shared.NodeEvaluation,
	rosterLen int,
	req shared.TaskRequirements,
	priority, preemptThreshold, slewNearbyMs int,
	now time.Time,
) []shared.SelectorResult {
	assignments := shared.SelectCohort(selectPool, req, now)
	if len(assignments) == 0 {
		if sel := shared.SelectBestNode(
			selectPool, req, priority, preemptThreshold, slewNearbyMs, now,
		); sel != nil {
			assignments = []shared.SelectorResult{*sel}
		}
	}
	// Roster miss / empty result → fall back to full fleet scan.
	if len(assignments) == 0 && rosterLen > 0 && len(selectPool) < len(nodes) {
		assignments = shared.SelectCohort(nodes, req, now)
		if len(assignments) == 0 {
			if sel := shared.SelectBestNode(
				nodes, req, priority, preemptThreshold, slewNearbyMs, now,
			); sel != nil {
				assignments = []shared.SelectorResult{*sel}
			}
		}
	}
	return assignments
}

// assignExec is the set of side effects assignOnce performs once nodes are
// picked: the Postgres claim / cohort / preempt-release writes, the Redis
// node-state + task-queue moves, and the MQTT assign publish. Production wiring
// is *pgRedisExec (pgxpool + go-redis + paho); tests pass an in-memory fake so
// the double-assign (#400), cohort-upload (#402) and drain/NOTIFY-parity (#401)
// paths run with no live infra and no Skipf.
type assignExec interface {
	// ClaimPrimary runs the pending→assigned CAS plus the primary
	// task_assignments row for assignments[0]. claimed=false means another
	// path won the race and this assign must abort (#400).
	ClaimPrimary(ctx context.Context, taskID int, nodeID string) (claimed bool, err error)
	// RecordCohort inserts a non-primary ('cohort') task_assignments row so
	// that member passes upload auth (#402).
	RecordCohort(ctx context.Context, taskID int, nodeID string) error
	// ReleaseCohort removes a cohort row whose command was not delivered.
	ReleaseCohort(ctx context.Context, taskID int, nodeID string) error
	// ReleasePreempted clears the victim task's assignment held by nodeID.
	ReleasePreempted(ctx context.Context, prevTaskID int, nodeID string) error
	// PublishAssign sends the assign_task / preempt_task MQTT command and
	// returns how long the publish took and any delivery error.
	PublishAssign(payload shared.NotifyPayload, sel shared.SelectorResult) (time.Duration, error)
	// NodeBusy marks nodeID busy on taskID/priority in Redis and drops
	// idle_since (orchestrator owns these fields — #404).
	NodeBusy(ctx context.Context, nodeID string, taskID, priority int)
	// PreemptedTaskToQueued moves a preempted victim task inflight→queued.
	PreemptedTaskToQueued(ctx context.Context, taskID, priority int)
	// TaskToInflight moves the just-assigned task queued→inflight.
	TaskToInflight(ctx context.Context, taskID, priority int)
	// Requeue parks an unassignable task back on the interrupt queue
	// (no eligible node, or a claim error).
	Requeue(ctx context.Context, taskID, priority int)
	// Flush applies any batched Redis writes.
	Flush(ctx context.Context)
}

// assignOnce performs the post-selection half of one assignment through the
// assignExec seam: claim the primary row (#400), record cohort rows (#402),
// publish MQTT, and move Redis node-state + task-queue entries. It is the
// single path the NOTIFY and drain entry points share (#401). Returns the
// assigned node IDs, or nil when nothing was assigned.
func assignOnce(
	ctx context.Context,
	exec assignExec,
	payload shared.NotifyPayload,
	assignments []shared.SelectorResult,
	nodesForMetrics []shared.NodeEvaluation,
	metrics *shared.MetricsCollector,
	sugar *zap.SugaredLogger,
	t *shared.Timer,
	now time.Time,
	pgNotifyLatencyMs float64,
) []string {
	if t == nil {
		t = shared.NewTimer("task_lifecycle")
	}

	if len(assignments) == 0 {
		exec.Requeue(ctx, payload.TaskID, payload.Priority)
		metrics.Emit(shared.TaskEvent{
			Event:       shared.EventTaskQueued,
			Timestamp:   now,
			TaskID:      payload.TaskID,
			Priority:    payload.Priority,
			QueueReason: "no_visible_node",
		})
		sugar.Infow("task queued (no eligible node)",
			"task_id", payload.TaskID,
			"reason", "no_visible_node",
		)
		return nil
	}

	// Claim first assignee in Postgres before MQTT/Redis so a second path cannot win (#400).
	primary := assignments[0]
	claimed, err := exec.ClaimPrimary(ctx, payload.TaskID, primary.NodeID)
	if err != nil {
		sugar.Warnw("persist task assignment", "task_id", payload.TaskID, "node", primary.NodeID, "err", err)
		exec.Requeue(ctx, payload.TaskID, payload.Priority)
		return nil
	}
	if !claimed {
		sugar.Infow("task already claimed; skip assign",
			"task_id", payload.TaskID,
			"wanted_node", primary.NodeID,
		)
		return nil
	}

	var mqttPublishMS float64
	for i, sel := range assignments {
		// assignments[0] already has its primary row from ClaimPrimary;
		// record every other cohort member so it passes upload auth (#402).
		if i > 0 {
			if err := exec.RecordCohort(ctx, payload.TaskID, sel.NodeID); err != nil {
				sugar.Warnw("persist cohort assignment", "task_id", payload.TaskID, "node", sel.NodeID, "err", err)
			}
		}
		publishDuration, publishErr := exec.PublishAssign(payload, sel)
		mqttPublishMS = float64(publishDuration.Milliseconds())
		if publishErr != nil {
			sugar.Warnw("publish task assignment", "task_id", payload.TaskID, "node", sel.NodeID, "err", publishErr)
			// MQTT delivery can be ambiguous after a timeout: a command may
			// already be in the broker or on the pier. Never roll back a
			// claimed task and immediately retry, because that can execute the
			// same task twice. Keep the claim for lease reclaim, retain any
			// successfully delivered members, and release only the failed
			// unpublished cohort row.
			if i > 0 {
				if releaseErr := exec.ReleaseCohort(ctx, payload.TaskID, sel.NodeID); releaseErr != nil {
					sugar.Errorw("release unpublished cohort assignment", "task_id", payload.TaskID, "node", sel.NodeID, "err", releaseErr)
				}
			}
			for _, delivered := range assignments[:i] {
				exec.NodeBusy(ctx, delivered.NodeID, payload.TaskID, payload.Priority)
			}
			exec.TaskToInflight(ctx, payload.TaskID, payload.Priority)
			exec.Flush(ctx)
			return nil
		}
	}

	for _, sel := range assignments {
		if sel.Preempting && sel.PrevTaskID != nil {
			if err := exec.ReleasePreempted(ctx, *sel.PrevTaskID, sel.NodeID); err != nil {
				sugar.Warnw("clear preempted assignment", "task_id", *sel.PrevTaskID, "err", err)
			}
			exec.PreemptedTaskToQueued(ctx, *sel.PrevTaskID, payload.Priority)
		}
		exec.NodeBusy(ctx, sel.NodeID, payload.TaskID, payload.Priority)
	}
	exec.TaskToInflight(ctx, payload.TaskID, payload.Priority)
	exec.Flush(ctx)
	t.Step("mqtt_publish")

	orchestratorLatency := time.Since(t.Start).Microseconds()
	nodeIDs := make([]string, len(assignments))
	for i, sel := range assignments {
		nodeIDs[i] = sel.NodeID
		emitAssignmentEvent(metrics, payload, sel, nodesForMetrics, orchestratorLatency, mqttPublishMS, pgNotifyLatencyMs, t)
	}
	sugar.Infow("task assigned",
		"task_id", payload.TaskID,
		"nodes", nodeIDs,
		"count", len(assignments),
	)
	t.Report(sugar, payload.TaskID, nodeIDs[0])
	return nodeIDs
}

// pgRedisExec is the production assignExec: Postgres writes via the existing
// persistTaskAssignment / insertCohortAssignment / clearTaskAssignment helpers,
// Redis node-state + queue moves batched into one pipeline, MQTT via paho.
type pgRedisExec struct {
	pool  *pgxpool.Pool
	rdb   *redis.Client
	mqtt  mqtt.Client
	sugar *zap.SugaredLogger
	pipe  redis.Pipeliner
}

func newPGRedisExec(pool *pgxpool.Pool, rdb *redis.Client, mqttClient mqtt.Client, sugar *zap.SugaredLogger) *pgRedisExec {
	return &pgRedisExec{pool: pool, rdb: rdb, mqtt: mqttClient, sugar: sugar, pipe: rdb.Pipeline()}
}

func (e *pgRedisExec) ClaimPrimary(ctx context.Context, taskID int, nodeID string) (bool, error) {
	return persistTaskAssignment(ctx, e.pool, taskID, nodeID)
}

func (e *pgRedisExec) RecordCohort(ctx context.Context, taskID int, nodeID string) error {
	return insertCohortAssignment(ctx, e.pool, taskID, nodeID)
}

func (e *pgRedisExec) ReleaseCohort(ctx context.Context, taskID int, nodeID string) error {
	return releaseCohortAssignment(ctx, e.pool, taskID, nodeID)
}

func (e *pgRedisExec) ReleasePreempted(ctx context.Context, prevTaskID int, nodeID string) error {
	return clearTaskAssignment(ctx, e.pool, prevTaskID, nodeID)
}

func (e *pgRedisExec) PublishAssign(payload shared.NotifyPayload, sel shared.SelectorResult) (time.Duration, error) {
	return publishTaskAssignment(e.mqtt, payload, sel)
}

func (e *pgRedisExec) NodeBusy(ctx context.Context, nodeID string, taskID, priority int) {
	nodeKey := fmt.Sprintf(shared.RedisNodeState, nodeID)
	e.pipe.HSet(ctx, nodeKey,
		"status", "busy",
		"current_task_id", taskID,
		"current_task_priority", priority,
	)
	e.pipe.HDel(ctx, nodeKey, "idle_since")
}

func (e *pgRedisExec) PreemptedTaskToQueued(ctx context.Context, taskID, priority int) {
	moveInflightToQueued(ctx, e.pipe, taskID, priority)
}

func (e *pgRedisExec) TaskToInflight(ctx context.Context, taskID, priority int) {
	moveQueuedToInflight(ctx, e.pipe, taskID, priority)
}

func (e *pgRedisExec) Requeue(ctx context.Context, taskID, priority int) {
	enqueueByLane(ctx, e.rdb, taskID, priority, lanes.LaneInterrupt)
}

func (e *pgRedisExec) Flush(ctx context.Context) {
	if _, err := e.pipe.Exec(ctx); err != nil {
		e.sugar.Warnw("redis state update", "err", err)
	}
}

// enqueueQueuedTask is the interrupt-lane enqueue (hot-path drain).
func enqueueQueuedTask(ctx context.Context, rdb *redis.Client, taskID, priority int) {
	enqueueByLane(ctx, rdb, taskID, priority, lanes.LaneInterrupt)
}

func moveQueuedToInflight(ctx context.Context, pipe redis.Pipeliner, taskID, priority int) {
	pipe.ZRem(ctx, shared.RedisQueuedTasks, taskID)
	pipe.ZRem(ctx, shared.RedisActiveTasks, taskID) // legacy cleanup
	pipe.ZAdd(ctx, shared.RedisInflightTasks, &redis.Z{
		Score:  float64(priority),
		Member: taskID,
	})
}

func moveInflightToQueued(ctx context.Context, pipe redis.Pipeliner, taskID, priority int) {
	pipe.ZRem(ctx, shared.RedisInflightTasks, taskID)
	pipe.ZAdd(ctx, shared.RedisQueuedTasks, &redis.Z{
		Score:  float64(priority),
		Member: taskID,
	})
}

func publishTaskAssignment(mqttClient mqtt.Client, payload shared.NotifyPayload, sel shared.SelectorResult) (time.Duration, error) {
	start := time.Now()
	cmdTopic := fmt.Sprintf(shared.TopicCommands, sel.NodeID)
	assign := assignPayloadFromNotify(payload)
	var cmd shared.Command
	var err error
	if sel.Preempting && sel.PrevTaskID != nil {
		cmd, err = shared.SealCommand("preempt_task", sel.NodeID, shared.PreemptTaskPayload{
			PrevTaskID: *sel.PrevTaskID,
			NewTask:    assign,
		})
	} else {
		cmd, err = shared.SealCommand("assign_task", sel.NodeID, assign)
	}
	if err != nil {
		return time.Since(start), fmt.Errorf("seal assignment for node %s: %w", sel.NodeID, err)
	}
	cmdBytes, err := json.Marshal(cmd)
	if err != nil {
		return time.Since(start), fmt.Errorf("marshal assignment for node %s: %w", sel.NodeID, err)
	}
	token := mqttClient.Publish(cmdTopic, 1, false, cmdBytes)
	if !token.WaitTimeout(2 * time.Second) {
		return time.Since(start), fmt.Errorf("publish assignment for node %s timed out", sel.NodeID)
	}
	if err := token.Error(); err != nil {
		return time.Since(start), fmt.Errorf("publish assignment for node %s: %w", sel.NodeID, err)
	}
	return time.Since(start), nil
}

// persistTaskAssignment claims a pending+unassigned row → status=assigned (#400)
// and writes the primary row into task_assignments in the same tx (#402).
// Returns claimed=false when another path already took the task.
func persistTaskAssignment(ctx context.Context, pool *pgxpool.Pool, taskID int, telescopeID string) (bool, error) {
	if pool == nil || telescopeID == "" || taskID <= 0 {
		return false, nil
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `
		UPDATE tasks
		SET assigned_telescope_id = $1,
		    status = $2,
		    last_assignment_attempt_at = NOW(),
		    updated_at = NOW()
		WHERE id = $3
		  AND status = $4
		  AND assigned_telescope_id IS NULL
	`, telescopeID, shared.TaskStatusAssigned, taskID, shared.TaskStatusPending)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO task_assignments (task_id, telescope_id, role, status, lease_expires_at)
		VALUES ($1, $2, 'primary', 'assigned', $3)
		ON CONFLICT (task_id, telescope_id)
		DO UPDATE SET role = 'primary', status = 'assigned',
		              lease_expires_at = EXCLUDED.lease_expires_at, updated_at = NOW()
	`, taskID, telescopeID, leaseExpiry()); err != nil {
		return false, err
	}

	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// insertCohortAssignment records a non-primary cohort member of a task so it
// passes upload auth (#402). Best-effort: the primary row is what gates the
// task claim; a cohort row failing only costs that member its upload path.
func insertCohortAssignment(ctx context.Context, pool *pgxpool.Pool, taskID int, telescopeID string) error {
	if pool == nil || telescopeID == "" || taskID <= 0 {
		return nil
	}
	_, err := pool.Exec(ctx, `
		INSERT INTO task_assignments (task_id, telescope_id, role, status, lease_expires_at)
		VALUES ($1, $2, 'cohort', 'assigned', $3)
		ON CONFLICT (task_id, telescope_id) DO NOTHING
	`, taskID, telescopeID, leaseExpiry())
	return err
}

// clearTaskAssignment drops the assignment when preempted if still held by this node.
func clearTaskAssignment(ctx context.Context, pool *pgxpool.Pool, taskID int, telescopeID string) error {
	if pool == nil || taskID <= 0 {
		return nil
	}
	if _, err := pool.Exec(ctx, `
		UPDATE tasks
		SET assigned_telescope_id = NULL,
		    status = $1,
		    updated_at = NOW()
		WHERE id = $2
		  AND (assigned_telescope_id IS NULL OR assigned_telescope_id = $3)
		  AND status IN ($4, $5)
	`, shared.TaskStatusPending, taskID, telescopeID,
		shared.TaskStatusAssigned, shared.TaskStatusInProgress); err != nil {
		return err
	}
	_, err := pool.Exec(ctx, `
		UPDATE task_assignments
		SET status = 'released', updated_at = NOW()
		WHERE task_id = $1 AND telescope_id = $2
		  AND status IN ('assigned', 'in_progress')
	`, taskID, telescopeID)
	return err
}

func releaseCohortAssignment(ctx context.Context, pool *pgxpool.Pool, taskID int, telescopeID string) error {
	if pool == nil || taskID <= 0 {
		return nil
	}
	_, err := pool.Exec(ctx, `
		UPDATE task_assignments
		SET status = 'released', updated_at = NOW()
		WHERE task_id = $1 AND telescope_id = $2
		  AND role = 'cohort' AND status IN ('assigned', 'in_progress')
	`, taskID, telescopeID)
	return err
}

func assignPayloadFromNotify(payload shared.NotifyPayload) shared.AssignTaskPayload {
	ht := shared.HandoffTaskFromNotify(payload)
	urg := shared.UrgencyForTask(ht, time.Now().UTC())
	out := shared.AssignTaskPayload{
		TaskID:          payload.TaskID,
		CampaignID:      payload.CampaignID,
		Priority:        payload.Priority,
		Name:            payload.Name,
		TargetRA:        payload.TargetRA,
		TargetDec:       payload.TargetDec,
		IntegrationTime: safeFloat64(payload.IntegrationTime),
		RequiredFilters: payload.RequiredFilters,
		MinAltitudeDeg:  payload.MinAltitudeDeg,
	}
	if urg != shared.UrgencyNone {
		out.HandoffUrgency = string(urg)
	}
	if payload.ScheduledEndAt != nil {
		s := payload.ScheduledEndAt.UTC().Format(time.RFC3339)
		out.ScheduledEndAt = &s
	}
	// On-pier researcher code (#470 step 3 / #516, step 5 / #518). Only present
	// when the campaign pack enabled pier_code; the pier verifies PierCode's
	// content hash on fetch and still gates on local consent (#517) and the
	// kill switch (#520).
	out.PierCodeGrants = payload.PierCodeGrants
	out.PierCode = payload.PierCode
	out.PierCodeDisabled = payload.PierCodeDisabled
	return out
}

func emitAssignmentEvent(
	metrics *shared.MetricsCollector,
	payload shared.NotifyPayload,
	sel shared.SelectorResult,
	nodes []shared.NodeEvaluation,
	orchestratorLatency int64,
	mqttPublishMS float64,
	pgNotifyLatencyMs float64,
	t *shared.Timer,
) {
	selNode := findNodeEval(nodes, sel.NodeID)
	evt := shared.TaskEvent{
		Event:                 shared.EventTaskAssigned,
		Timestamp:             time.Now().UTC(),
		TaskID:                payload.TaskID,
		Priority:              payload.Priority,
		NodeID:                sel.NodeID,
		SlewTimeMs:            sel.SlewTimeMs,
		IsPreemption:          sel.Preempting,
		PrevTaskID:            sel.PrevTaskID,
		SelectorReason:        sel.Reason,
		OrchestratorLatencyUs: orchestratorLatency,
		RequiredFilters:       payload.RequiredFilters,
		CampaignID:            payload.CampaignID,
		MQTTPublishLatencyMS:  &mqttPublishMS,
		PGNotifyLatencyMS:     &pgNotifyLatencyMs,
	}
	if payload.EmergencyHandoffRequestedAt != nil {
		evt.HandoffRequested = true
	}
	shared.RecordTimer(&evt, t)
	if selNode != nil {
		evt.NodeQualityTier = selNode.QualityTier
		if selNode.MountSlewRateDegS != nil {
			evt.NodeSlewRateDegS = *selNode.MountSlewRateDegS
		}
		evt.NodeFilters = selNode.AvailableFilters
	}
	if sel.Preempting && sel.PrevTaskID != nil && selNode != nil && selNode.CurrentTaskPriority != nil {
		evt.PriorityDiff = *selNode.CurrentTaskPriority - payload.Priority
		evt.Event = shared.EventTaskPreempted
	}
	metrics.Emit(evt)
}

func drainLoop(ctx context.Context, pool *pgxpool.Pool, rdb *redis.Client, mqttClient mqtt.Client,
	metrics *shared.MetricsCollector, sugar *zap.SugaredLogger,
	preemptThreshold int, slewNearbyMs int) {

	for {
		next, err := rdb.ZPopMin(ctx, shared.RedisQueuedTasks).Result()
		if err != nil || len(next) == 0 {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		taskID, err := redisZMemberInt(next[0].Member)
		if err != nil {
			sugar.Warnw("drain: bad redis task member", "member", next[0].Member, "err", err)
			continue
		}
		priority := int(next[0].Score)

		payload, requeue, err := fetchTaskNotifyPayload(ctx, pool, taskID)
		if err != nil {
			sugar.Warnw("drain: task lookup failed", "task_id", taskID, "err", err)
			time.Sleep(200 * time.Millisecond)
			continue
		}
		if requeue {
			enqueueQueuedTask(ctx, rdb, taskID, priority)
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if payload == nil {
			// Not assignable (already assigned / terminal) — leave out of queue.
			continue
		}
		assignTask(ctx, pool, *payload, rdb, mqttClient, metrics, sugar, preemptThreshold, slewNearbyMs, nil, 0)
	}
}

func filterNodesByCampaign(nodes []shared.NodeEvaluation, campaignID string) []shared.NodeEvaluation {
	if campaignID == "" {
		return nodes
	}
	out := make([]shared.NodeEvaluation, 0, len(nodes))
	for _, n := range nodes {
		if campaign.NodeServesCampaign(n.EnabledCampaignIDs, campaignID) {
			out = append(out, n)
		}
	}
	return out
}

func redisZMemberInt(member interface{}) (int, error) {
	switch v := member.(type) {
	case float64:
		return int(v), nil
	case int64:
		return int(v), nil
	case int:
		return v, nil
	case string:
		return strconv.Atoi(v)
	default:
		return 0, fmt.Errorf("unexpected member type %T", member)
	}
}

func fetchAllNodeEvals(ctx context.Context, rdb *redis.Client, sugar *zap.SugaredLogger) ([]shared.NodeEvaluation, error) {
	members, err := rdb.SMembers(ctx, shared.RedisActiveNodes).Result()
	if err != nil {
		return nil, err
	}
	if len(members) == 0 {
		return nil, nil
	}

	pipe := rdb.Pipeline()
	type fetch struct {
		nodeID string
		state  *redis.StringStringMapCmd
		meta   *redis.StringStringMapCmd
	}
	var fetches []fetch
	for _, nodeID := range members {
		stateKey := fmt.Sprintf(shared.RedisNodeState, nodeID)
		metaKey := fmt.Sprintf(shared.RedisNodeMeta, nodeID)
		f := fetch{
			nodeID: nodeID,
			state:  pipe.HGetAll(ctx, stateKey),
			meta:   pipe.HGetAll(ctx, metaKey),
		}
		fetches = append(fetches, f)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, err
	}

	nodes := make([]shared.NodeEvaluation, 0, len(fetches))
	now := time.Now().UTC()
	for _, f := range fetches {
		state, err := f.state.Result()
		if err != nil || len(state) == 0 {
			continue
		}
		meta, _ := f.meta.Result()

		ne := shared.NodeEvaluation{
			NodeID: f.nodeID,
			Status: state["status"],
			Power:  redisFloat(meta, "power", 0),
		}
		ne.LimitingMagnitude = redisFloatPtr(meta, "limiting_magnitude")
		ne.CurrentTaskID = redisIntPtr(state, "current_task_id")
		ne.CurrentTaskPriority = redisIntPtr(state, "current_task_priority")
		ne.EstimatedStartupMS = redisInt(state, "estimated_startup_ms", 0)
		ne.MountAltDeg = redisFloatPtr(state, "mount_alt_deg")
		ne.MountAzDeg = redisFloatPtr(state, "mount_az_deg")
		ne.QualityTier = meta["quality_tier"]
		ne.ReliabilityScore = redisFloat(meta, "reliability_score", 0)
		ne.SiteLat = redisFloatPtr(meta, "site_lat")
		ne.SiteLon = redisFloatPtr(meta, "site_lon")
		ne.MountSlewRateDegS = redisFloatPtr(meta, "mount_slew_rate_deg_s")
		ne.ApertureMM = redisFloatPtr(meta, "aperture_mm")
		ne.FocalLengthMM = redisFloatPtr(meta, "focal_length_mm")
		ne.PixelSizeUM = redisFloatPtr(meta, "pixel_size_um")
		ne.FOVWidthArcmin = redisFloatPtr(meta, "fov_width_arcmin")
		ne.FOVHeightArcmin = redisFloatPtr(meta, "fov_height_arcmin")
		ne.MountType = redisIntPtr(meta, "mount_type")
		ne.MaxStableExposureS = redisFloatPtr(meta, "max_stable_exposure_s")
		ne.SiteSeeingArcsec = redisFloatPtr(meta, "median_seeing_arcsec")
		ne.IdleSinceMinutes = idleMinutes(state["idle_since"], now)
		ne.PlanRemainingSec = positiveFloatPtr(state, "plan_remaining_sec")
		ne.AvailableFilters = redisStrings(meta["available_filters"])
		mountLimits, ok := parseMountLimits(meta["mount_limits"])
		if !ok {
			sugar.Warnw("invalid node mount limits", "node_id", f.nodeID)
			continue
		}
		horizonProfile, ok := parseHorizonProfile(meta["horizon_profile"])
		if !ok {
			sugar.Warnw("invalid node horizon profile", "node_id", f.nodeID)
			continue
		}
		obstructionMask, err := shared.ParseObstructionMask(meta["obstruction_mask"])
		if err != nil {
			// A malformed obstruction mask must not silently become an
			// unrestricted node. Leave this node out of the assignment pool.
			sugar.Warnw("invalid node obstruction mask", "node_id", f.nodeID, "err", err)
			continue
		}
		ne.ObstructionMask = obstructionMask
		ne.MountLimits = mountLimits
		ne.HorizonProfile = horizonProfile
		ne.EnabledCampaignIDs = redisStrings(meta["enabled_campaign_ids"])
		ne.IsEmulator = meta["is_emulator"] == "true" || meta["is_emulator"] == "1" || strings.HasPrefix(f.nodeID, "emu_")
		nodes = append(nodes, ne)
	}
	return nodes, nil
}

func redisInt(values map[string]string, key string, fallback int) int {
	v, err := strconv.Atoi(values[key])
	if err != nil {
		return fallback
	}
	return v
}

func redisIntPtr(values map[string]string, key string) *int {
	v := values[key]
	if v == "" || v == "<nil>" {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return nil
	}
	return &n
}

func redisFloat(values map[string]string, key string, fallback float64) float64 {
	v, err := strconv.ParseFloat(values[key], 64)
	if err != nil {
		return fallback
	}
	return v
}

func redisFloatPtr(values map[string]string, key string) *float64 {
	v := values[key]
	if v == "" {
		return nil
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return nil
	}
	return &n
}

func positiveFloatPtr(values map[string]string, key string) *float64 {
	n := redisFloatPtr(values, key)
	if n == nil || *n <= 0 {
		return nil
	}
	return n
}

func idleMinutes(value string, now time.Time) *float64 {
	if value == "" {
		return nil
	}
	ts, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil
	}
	mins := now.Sub(ts).Minutes()
	if mins < 0 {
		mins = 0
	}
	return &mins
}

func redisStrings(value string) []string {
	if value == "" {
		return nil
	}
	var values []string
	if err := json.Unmarshal([]byte(value), &values); err != nil {
		return nil
	}
	return values
}

func parseMountLimits(raw string) (*shared.MountLimits, bool) {
	if strings.TrimSpace(raw) == "" {
		return nil, true
	}
	limits := shared.ParseMountLimits(raw)
	return limits, limits != nil
}

func parseHorizonProfile(raw string) (*shared.HorizonProfile, bool) {
	if strings.TrimSpace(raw) == "" {
		return nil, true
	}
	profile := shared.ParseHorizonProfile(raw)
	return profile, profile != nil
}

func findNodeEval(nodes []shared.NodeEvaluation, nodeID string) *shared.NodeEvaluation {
	for i := range nodes {
		if nodes[i].NodeID == nodeID {
			return &nodes[i]
		}
	}
	return nil
}

func safeFloat64(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}
