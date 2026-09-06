package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/go-redis/redis/v8"
	pgx "github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/saucepan/hotpath/shared"
	"go.uber.org/zap"
)

// startNodeStateReconciler is the collector's *only* sanctioned path for
// clearing the orchestrator-owned current_task_id / current_task_priority
// fields from Redis node_state (#404).
//
// The collector must never wipe those fields on a bare idle heartbeat: a node
// that reports one idle beat in the gap between the MQTT assign and the start
// of its exposure loop would lose its assignment marker and become eligible
// for a second assignment, while preemption scoring (the orchestrator reads
// current_task_priority out of node_state) would go blind.
//
// The fields are cleared only when all three hold:
//  1. telemetry says the node is idle, and
//  2. telemetry carries no current_task_id of its own (tel.CurrentTaskID == nil), and
//  3. Postgres shows the task recorded in Redis as terminal (completed / expired).
//
// Otherwise the fields are left untouched; #403's lease-reclaim loop is the
// backstop for a node that dies mid-task and never recovers.
//
// Fail-open, matching startLeaseRenewer: no PG_DSN → reconciler simply off.
// It adds its own telemetry subscription (paho dispatches to every matching
// route) and its own pool, so cmd/collector/main.go is untouched beyond one
// call. Redis is shared with main() rather than reopened.
func startNodeStateReconciler(ctx context.Context, rdb *redis.Client, client mqtt.Client, log *zap.SugaredLogger) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		log.Info("node-state reconciler disabled (PG_DSN unset)")
		return
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Warnw("node-state reconciler: pg connect failed, reconciler disabled", "err", err)
		return
	}
	store := &redisNodeStateStore{rdb: rdb}
	tasks := &pgTaskStatusReader{pool: pool}

	token := client.Subscribe(shared.SubscribeFilter(shared.TopicTelemetry), 1, func(_ mqtt.Client, msg mqtt.Message) {
		var tel shared.Telemetry
		if err := json.Unmarshal(msg.Payload(), &tel); err != nil {
			return
		}
		nodeID, ok := bindTopicNodeID(msg.Topic(), shared.TopicPrefix(shared.TopicTelemetry), tel.NodeID)
		if !ok {
			return
		}
		reconcileNodeState(ctx, store, tasks, nodeID, tel, log)
	})
	if !waitForSubscription(token, log, "node-state reconciliation telemetry") {
		pool.Close()
		return
	}
	log.Info("node-state reconciler running (idle + no telemetry task id + DB-terminal task → clear current_task_*)")
}

// nodeStateStore is the Redis seam the reconcile logic needs — small enough
// that a test can substitute a map-backed fake with no miniredis.
type nodeStateStore interface {
	// currentTaskID returns the stored current_task_id for the node, or ""
	// when the field is absent.
	currentTaskID(ctx context.Context, nodeID string) (string, error)
	// clearCurrentTask drops current_task_id + current_task_priority and puts
	// scheduling status back to idle, but only if the stored current_task_id
	// still equals wantTaskID (guards against clobbering a fresher assign).
	clearCurrentTask(ctx context.Context, nodeID, wantTaskID string) error
}

// taskStatusReader is the Postgres seam: does this task id exist, and what is
// its lifecycle status.
type taskStatusReader interface {
	taskStatus(ctx context.Context, taskID int) (status string, found bool, err error)
}

// reconcileNodeState applies the three-part handoff test to one heartbeat.
func reconcileNodeState(ctx context.Context, store nodeStateStore, tasks taskStatusReader, nodeID string, tel shared.Telemetry, log *zap.SugaredLogger) {
	if tel.Status != "idle" || tel.CurrentTaskID != nil {
		return
	}
	stored, err := store.currentTaskID(ctx, nodeID)
	if err != nil {
		log.Debugw("node-state reconcile: redis read failed", "node", nodeID, "err", err)
		return
	}
	if stored == "" {
		return // nothing assigned in Redis — nothing to reconcile
	}
	taskID, err := strconv.Atoi(stored)
	if err != nil {
		log.Debugw("node-state reconcile: unparseable current_task_id", "node", nodeID, "value", stored)
		return
	}
	status, found, err := tasks.taskStatus(ctx, taskID)
	if err != nil {
		log.Debugw("node-state reconcile: pg read failed", "node", nodeID, "task_id", taskID, "err", err)
		return
	}
	if !found {
		return // unknown task — leave it for the lease-reclaim backstop
	}
	if status != shared.TaskStatusCompleted && status != shared.TaskStatusExpired {
		return // task still live — the assignment marker stays
	}
	if err := store.clearCurrentTask(ctx, nodeID, stored); err != nil {
		log.Debugw("node-state reconcile: clear failed", "node", nodeID, "err", err)
		return
	}
	log.Infow("node-state reconcile: cleared current_task_* after DB-confirmed terminal task",
		"node", nodeID, "task_id", taskID, "task_status", status)
}

// ── concrete seam implementations ──────────────────────────────────────

type redisNodeStateStore struct{ rdb *redis.Client }

func (s *redisNodeStateStore) currentTaskID(ctx context.Context, nodeID string) (string, error) {
	key := fmt.Sprintf(shared.RedisNodeState, nodeID)
	v, err := s.rdb.HGet(ctx, key, "current_task_id").Result()
	if err == redis.Nil {
		return "", nil
	}
	return v, err
}

func (s *redisNodeStateStore) clearCurrentTask(ctx context.Context, nodeID, wantTaskID string) error {
	key := fmt.Sprintf(shared.RedisNodeState, nodeID)
	for attempt := 0; attempt < 3; attempt++ {
		err := s.rdb.Watch(ctx, func(tx *redis.Tx) error {
			cur, err := tx.HGet(ctx, key, "current_task_id").Result()
			if err == redis.Nil {
				return nil
			}
			if err != nil {
				return err
			}
			if cur != wantTaskID {
				return nil
			}
			_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				pipe.HDel(ctx, key, "current_task_id", "current_task_priority", "idle_since")
				pipe.HSet(ctx, key, "status", shared.NodeStatusIdle)
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

type pgTaskStatusReader struct{ pool *pgxpool.Pool }

func (r *pgTaskStatusReader) taskStatus(ctx context.Context, taskID int) (string, bool, error) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var status string
	err := r.pool.QueryRow(cctx, `SELECT status FROM tasks WHERE id = $1`, taskID).Scan(&status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	return status, true, nil
}
