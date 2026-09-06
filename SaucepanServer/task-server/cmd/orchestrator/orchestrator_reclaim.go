package main

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/go-redis/redis/v8"
	pgxpool "github.com/jackc/pgx/v5/pgxpool"
	"github.com/saucepan/hotpath/shared"
	"go.uber.org/zap"
)

// Assignment leases & reclaim (#403).
//
// Every task_assignments row carries a lease_expires_at (#402 declared the
// column). It is set on assign (persistTaskAssignment / insertCohortAssignment)
// to now()+TASK_LEASE_TTL and pushed forward by the collector whenever the
// assignee's telemetry still reports current_task_id == this task
// (cmd/collector/lease_renew.go). A pier that dies mid-plan stops renewing, the
// lease lapses, and reclaimLoop below requeues the task so another node can take
// it — with an exponential backoff so a task that keeps failing is not
// hot-looped, and a terminal 'expired' after TASK_MAX_FAILURES.
//
// The pure decision helpers (leaseExpired / backoffWindow / taskInBackoff /
// leaseReclaimTerminal / decideReclaim) hold the policy and are unit-tested with
// a fake clock; reclaimLoop is the thin SQL/Redis executor around them.

// reclaimNow is the reclaim path's clock. Overridden in tests for a fake clock.
var reclaimNow = time.Now

// leaseCfg holds the lease TTL + reclaim knobs. Set once in main() from the
// environment; zero-value (tests that never call main) disables backoff and
// falls back to a 15m TTL via leaseExpiry().
var leaseCfg leaseConfig

type leaseConfig struct {
	// TTL is how far ahead lease_expires_at is set on assign and on renewal.
	TTL time.Duration
	// Interval is how often reclaimLoop sweeps for expired leases.
	Interval time.Duration
	// BackoffBase is the base of the exponential re-selection backoff:
	// min(BackoffBase * 2^failure_count, 30m) measured from
	// tasks.last_assignment_attempt_at.
	BackoffBase time.Duration
	// MaxFailures is the lease-expiry count past which a task goes terminal
	// 'expired' and is never requeued again.
	MaxFailures int
}

func leaseConfigFromEnv() leaseConfig {
	return leaseConfig{
		TTL:         envDuration("TASK_LEASE_TTL", 15*time.Minute),
		Interval:    envDuration("TASK_RECLAIM_INTERVAL", 60*time.Second),
		BackoffBase: envDuration("TASK_RECLAIM_BACKOFF_BASE", 2*time.Minute),
		MaxFailures: envInt("TASK_MAX_FAILURES", 5),
	}
}

// leaseExpiry is the timestamp written into lease_expires_at on assign/renewal.
func leaseExpiry() time.Time {
	ttl := leaseCfg.TTL
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	return reclaimNow().UTC().Add(ttl)
}

// backoffWindowCap bounds the exponential backoff regardless of failure count.
const backoffWindowCap = 30 * time.Minute

// backoffWindow = min(base * 2^failureCount, 30m). base<=0 disables it.
func backoffWindow(base time.Duration, failureCount int) time.Duration {
	if base <= 0 {
		return 0
	}
	if failureCount < 0 {
		failureCount = 0
	}
	if failureCount > 20 { // guard against int64 overflow on the shift
		return backoffWindowCap
	}
	w := base << uint(failureCount)
	if w <= 0 || w > backoffWindowCap {
		return backoffWindowCap
	}
	return w
}

// taskInBackoff reports whether a task last attempted at lastAttempt is still
// inside its exponential backoff window and must not be re-selected yet.
func taskInBackoff(lastAttempt time.Time, base time.Duration, failureCount int, now time.Time) bool {
	if lastAttempt.IsZero() {
		return false
	}
	w := backoffWindow(base, failureCount)
	if w <= 0 {
		return false
	}
	return now.Sub(lastAttempt) < w
}

// leaseExpired reports whether a lease timestamp has lapsed as of now. A NULL
// (zero) lease_expires_at is treated as not-expired (never leased / legacy row).
func leaseExpired(leaseExpiresAt, now time.Time) bool {
	return !leaseExpiresAt.IsZero() && leaseExpiresAt.Before(now)
}

// leaseReclaimTerminal reports whether a task that has now lapsed failureCount
// times (post-increment) has exhausted its retries.
func leaseReclaimTerminal(failureCount, maxFailures int) bool {
	return maxFailures > 0 && failureCount >= maxFailures
}

// reclaimAction is the decision for a single expired assignment row.
type reclaimAction struct {
	// RequeueTask: no active assignee remains → task back to 'pending'.
	RequeueTask bool
	// TerminalExpire: no active assignee remains and MaxFailures reached →
	// task to terminal 'expired', never requeued.
	TerminalExpire bool
}

// decideReclaim maps (assignees still active on the task, the task's failure
// count after this lapse is counted, MaxFailures) onto what to do with the task
// row. The expired assignment row itself is always marked 'expired' by the
// caller; this only governs the parent task.
func decideReclaim(activeAssigneesLeft, newFailureCount, maxFailures int) reclaimAction {
	if activeAssigneesLeft > 0 {
		return reclaimAction{} // a cohort member died; the task lives on
	}
	if leaseReclaimTerminal(newFailureCount, maxFailures) {
		return reclaimAction{TerminalExpire: true}
	}
	return reclaimAction{RequeueTask: true}
}

// reclaimLoop sweeps expired leases every leaseCfg.Interval. Started from main()
// alongside drainLoop.
func reclaimLoop(ctx context.Context, pool *pgxpool.Pool, rdb *redis.Client,
	metrics *shared.MetricsCollector, sugar *zap.SugaredLogger) {

	interval := leaseCfg.Interval
	if interval <= 0 {
		interval = 60 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	sugar.Infow("lease reclaim loop started",
		"interval", interval,
		"ttl", leaseCfg.TTL,
		"backoff_base", leaseCfg.BackoffBase,
		"max_failures", leaseCfg.MaxFailures,
	)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := reclaimExpiredLeases(ctx, pool, rdb, metrics, sugar); err != nil {
				sugar.Warnw("lease reclaim sweep", "err", err)
			}
		}
	}
}

// reclaimExpiredLeases finds every assigned/in_progress assignment row whose
// lease has lapsed and reclaims each one in its own transaction.
func reclaimExpiredLeases(ctx context.Context, pool *pgxpool.Pool, rdb *redis.Client,
	metrics *shared.MetricsCollector, sugar *zap.SugaredLogger) error {

	if pool == nil {
		return nil
	}
	rows, err := pool.Query(ctx, `
		SELECT task_id, telescope_id
		FROM task_assignments
		WHERE status IN ('assigned', 'in_progress')
		  AND lease_expires_at IS NOT NULL
		  AND lease_expires_at < $1
	`, reclaimNow().UTC())
	if err != nil {
		return err
	}
	type expired struct {
		taskID int
		nodeID string
	}
	var stale []expired
	for rows.Next() {
		var e expired
		if err := rows.Scan(&e.taskID, &e.nodeID); err != nil {
			continue
		}
		stale = append(stale, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, e := range stale {
		if err := reclaimOne(ctx, pool, rdb, metrics, sugar, e.taskID, e.nodeID); err != nil {
			sugar.Warnw("lease reclaim", "task_id", e.taskID, "node", e.nodeID, "err", err)
		}
	}
	return nil
}

// reclaimOne expires one lapsed assignment row and, if it was the task's last
// active assignee, requeues the task (or terminally expires it past
// TASK_MAX_FAILURES). Postgres first (one tx), then best-effort Redis + metrics.
func reclaimOne(ctx context.Context, pool *pgxpool.Pool, rdb *redis.Client,
	metrics *shared.MetricsCollector, sugar *zap.SugaredLogger, taskID int, nodeID string) error {

	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `
		UPDATE task_assignments
		SET status = 'expired', updated_at = NOW()
		WHERE task_id = $1 AND telescope_id = $2
		  AND status IN ('assigned', 'in_progress')
	`, taskID, nodeID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		// Renewed or already resolved between the sweep read and this tx;
		// the deferred Rollback unwinds the empty tx.
		return nil
	}

	var activeLeft int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*) FROM task_assignments
		WHERE task_id = $1 AND status IN ('assigned', 'in_progress')
	`, taskID).Scan(&activeLeft); err != nil {
		return err
	}

	var (
		curFailure int
		priority   int
		taskStatus string
	)
	if err := tx.QueryRow(ctx, `
		SELECT status, failure_count, priority FROM tasks WHERE id = $1 FOR UPDATE
	`, taskID).Scan(&taskStatus, &curFailure, &priority); err != nil {
		return err
	}
	// The assignment sweep can race with completion/cancellation. The row
	// lock above makes the lifecycle decision authoritative: only an active
	// parent may be moved back to pending or expired. The stale assignment is
	// still marked expired, and Redis cleanup below still frees the node.
	act := reclaimAction{}
	newFailure := curFailure
	if taskStatus == shared.TaskStatusAssigned || taskStatus == shared.TaskStatusInProgress {
		newFailure++
		act = decideReclaim(activeLeft, newFailure, leaseCfg.MaxFailures)
	}

	switch {
	case act.RequeueTask:
		if _, err := tx.Exec(ctx, `
			UPDATE tasks
			SET status = 'pending',
			    assigned_telescope_id = NULL,
			    failure_count = $2,
			    last_assignment_attempt_at = NOW(),
			    updated_at = NOW()
			WHERE id = $1
		`, taskID, newFailure); err != nil {
			return err
		}
	case act.TerminalExpire:
		if _, err := tx.Exec(ctx, `
			UPDATE tasks
			SET status = 'expired',
			    assigned_telescope_id = NULL,
			    failure_count = $2,
			    last_assignment_attempt_at = NOW(),
			    updated_at = NOW()
			WHERE id = $1
		`, taskID, newFailure); err != nil {
			return err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	// Redis: free the dead node and move the task queue↔inflight (best-effort).
	if rdb != nil {
		if err := clearReclaimedNode(ctx, rdb, nodeID, taskID); err != nil {
			sugar.Warnw("lease reclaim node-state cleanup", "task_id", taskID, "node", nodeID, "err", err)
		}
		pipe := rdb.Pipeline()
		switch {
		case act.RequeueTask:
			moveInflightToQueued(ctx, pipe, taskID, priority)
		case act.TerminalExpire:
			pipe.ZRem(ctx, shared.RedisInflightTasks, taskID)
			pipe.ZRem(ctx, shared.RedisQueuedTasks, taskID)
		}
		if _, err := pipe.Exec(ctx); err != nil {
			sugar.Warnw("lease reclaim redis update", "task_id", taskID, "err", err)
		}
	}

	switch {
	case act.RequeueTask:
		if metrics != nil {
			metrics.Emit(shared.TaskEvent{
				Event:       shared.EventTaskQueued,
				Timestamp:   reclaimNow().UTC(),
				TaskID:      taskID,
				Priority:    priority,
				QueueReason: "lease_expired",
			})
		}
		sugar.Infow("task reclaimed after lease expiry",
			"task_id", taskID, "dead_node", nodeID, "failure_count", newFailure)
	case act.TerminalExpire:
		if metrics != nil {
			metrics.Emit(shared.TaskEvent{
				Event:       shared.EventTaskQueued,
				Timestamp:   reclaimNow().UTC(),
				TaskID:      taskID,
				Priority:    priority,
				QueueReason: "lease_expired_terminal",
			})
		}
		sugar.Warnw("task terminally expired after repeated lease loss",
			"task_id", taskID, "dead_node", nodeID, "failure_count", newFailure,
			"max_failures", leaseCfg.MaxFailures)
	default:
		sugar.Infow("cohort assignee released after lease expiry",
			"task_id", taskID, "dead_node", nodeID, "active_assignees_left", activeLeft)
	}
	return nil
}

// clearReclaimedNode removes a stale lease marker only while the node still
// points at the reclaimed task. A fresh assignment can arrive between the
// SQL reclaim and this Redis cleanup, so the compare-and-delete must be
// atomic with the status update.
func clearReclaimedNode(ctx context.Context, rdb *redis.Client, nodeID string, taskID int) error {
	key := fmt.Sprintf(shared.RedisNodeState, nodeID)
	want := strconv.Itoa(taskID)
	for attempt := 0; attempt < 3; attempt++ {
		err := rdb.Watch(ctx, func(tx *redis.Tx) error {
			current, err := tx.HGet(ctx, key, "current_task_id").Result()
			if err == redis.Nil {
				return nil
			}
			if err != nil {
				return err
			}
			if current != want {
				return nil
			}
			_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				pipe.HSet(ctx, key, "status", shared.NodeStatusIdle)
				pipe.HDel(ctx, key, "current_task_id", "current_task_priority", "idle_since")
				return nil
			})
			return err
		}, key)
		if err != redis.TxFailedErr {
			return err
		}
	}
	return redis.TxFailedErr
}
