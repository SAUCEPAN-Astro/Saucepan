package main

import (
	"context"
	"encoding/json"
	"os"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/saucepan/hotpath/shared"
	"go.uber.org/zap"
)

// startLeaseRenewer pushes task_assignments.lease_expires_at forward while an
// assignee's telemetry still reports it is on the task (#403). Without this,
// every lease lapses after TASK_LEASE_TTL and the orchestrator's reclaimLoop
// requeues live work. The TTL contract is single-sourced in the orchestrator
// (cmd/orchestrator/orchestrator_reclaim.go leaseConfigFromEnv); the collector
// reads the same TASK_LEASE_TTL with the same 15m default.
//
// Fail-open, matching startCampaignBoardBridge: no PG_DSN → renewer simply off.
// It adds its own telemetry subscription (paho dispatches to every matching
// route) and its own pool so cmd/collector/main.go is untouched beyond one call.
func startLeaseRenewer(ctx context.Context, client mqtt.Client, log *zap.SugaredLogger) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		log.Info("lease renewer disabled (PG_DSN unset)")
		return
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Warnw("lease renewer: pg connect failed, renewer disabled", "err", err)
		return
	}
	ttl := leaseRenewTTL()

	token := client.Subscribe(shared.SubscribeFilter(shared.TopicTelemetry), 1, func(_ mqtt.Client, msg mqtt.Message) {
		var tel shared.Telemetry
		if err := json.Unmarshal(msg.Payload(), &tel); err != nil {
			return
		}
		nodeID, ok := bindTopicNodeID(msg.Topic(), shared.TopicPrefix(shared.TopicTelemetry), tel.NodeID)
		if !ok || tel.CurrentTaskID == nil {
			return
		}
		renewTaskLease(ctx, pool, *tel.CurrentTaskID, nodeID, ttl, log)
	})
	if !waitForSubscription(token, log, "lease-renewal telemetry") {
		pool.Close()
		return
	}
	log.Infow("lease renewer running (telemetry current_task_id → task_assignments.lease_expires_at)", "ttl", ttl)
}

// renewTaskLease bumps the lease for one (task, node) pair when it is still an
// active assignee. A no-op row count (task reassigned, completed, unknown) is
// silently fine.
func renewTaskLease(ctx context.Context, pool *pgxpool.Pool, taskID int, nodeID string, ttl time.Duration, log *zap.SugaredLogger) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := pool.Exec(cctx, `
		UPDATE task_assignments
		SET lease_expires_at = NOW() + make_interval(secs => $3), updated_at = NOW()
		WHERE task_id = $1 AND telescope_id = $2
		  AND status IN ('assigned', 'in_progress')
	`, taskID, nodeID, ttl.Seconds())
	if err != nil {
		log.Debugw("lease renew skipped", "task_id", taskID, "node", nodeID, "err", err)
	}
}

// leaseRenewTTL mirrors the orchestrator's TASK_LEASE_TTL (default 15m).
func leaseRenewTTL() time.Duration {
	if v := os.Getenv("TASK_LEASE_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 15 * time.Minute
}
