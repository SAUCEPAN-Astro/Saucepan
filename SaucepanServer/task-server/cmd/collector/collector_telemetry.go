package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/eclipse/paho.mqtt.golang"
	"github.com/go-redis/redis/v8"
	hpmetrics "github.com/saucepan/hotpath/internal/metrics"
	"github.com/saucepan/hotpath/shared"
	"go.uber.org/zap"
)

// startTelemetrySubscriber sets up the /telemetry/+ MQTT subscription handler.
func startTelemetrySubscriber(ctx context.Context, client mqtt.Client, rdb *redis.Client, edgeThrottle *edgeObsThrottle, sugar *zap.SugaredLogger) {
	token := client.Subscribe(shared.SubscribeFilter(shared.TopicTelemetry), 1, func(c mqtt.Client, msg mqtt.Message) {
		t := shared.NewTimer("telemetry_ingest")
		var tel shared.Telemetry
		if err := json.Unmarshal(msg.Payload(), &tel); err != nil {
			sugar.Warnw("bad telemetry", "payload_bytes", len(msg.Payload()), "err", err)
			return
		}
		nodeStatus, validStatus := telemetryNodeStatus(tel.Status)
		if !validStatus {
			sugar.Warnw("bad telemetry status", "node_id", tel.NodeID, "status", tel.Status)
			return
		}
		nodeID, ok := bindTopicNodeID(msg.Topic(), shared.TopicPrefix(shared.TopicTelemetry), tel.NodeID)
		if !ok {
			sugar.Warnw(formatReject(msg.Topic(), "topic/json node_id mismatch"),
				"json_node_id", tel.NodeID)
			return
		}
		tel.NodeID = nodeID
		t.Step("json_unmarshal")
		stateKey := fmt.Sprintf(shared.RedisNodeState, tel.NodeID)
		prevStatus, _ := rdb.HGet(ctx, stateKey, "telemetry_status").Result()
		estStartup := estimateStartupMS(tel)
		t.Step("pre_compute")
		pipe := rdb.Pipeline()
		pipe.HSet(ctx, stateKey, map[string]interface{}{
			"node_id":              tel.NodeID,
			"status":               nodeStatus,
			"load_pct":             tel.LoadPct,
			"estimated_startup_ms": estStartup,
			"completed_files":      tel.CompletedFiles,
			"memory_avail_mb":      tel.MemoryAvailMB,
			"last_seen":            time.Now().UTC().Format(time.RFC3339),
		})
		pipe.HSet(ctx, stateKey, "telemetry_status", tel.Status)
		if tel.CurrentTaskID != nil {
			pipe.HSet(ctx, stateKey, "telemetry_task_id", *tel.CurrentTaskID)
		} else {
			pipe.HDel(ctx, stateKey, "telemetry_task_id")
		}
		// NOTE (#404): the collector no longer wipes current_task_id /
		// current_task_priority on a bare idle heartbeat. Those fields are
		// orchestrator-owned; clearing them here on a single idle beat in the
		// gap between MQTT assign and the start of the exposure loop opened a
		// double-assign window and blinded preemption scoring. The defined
		// handoff (idle + no telemetry task id + DB shows the stored task
		// terminal) lives in startNodeStateReconciler; #403's lease reclaim is
		// the backstop for a node that dies mid-task.
		pipe.Expire(ctx, stateKey, shared.StateTTLSeconds*time.Second)
		pipe.SAdd(ctx, shared.RedisActiveNodes, tel.NodeID)
		pipe.Expire(ctx, shared.RedisActiveNodes, shared.StateTTLSeconds*time.Second*2)
		if tel.MountAltDeg != nil && tel.MountAzDeg != nil {
			pipe.HSet(ctx, stateKey, "mount_alt_deg", *tel.MountAltDeg)
			pipe.HSet(ctx, stateKey, "mount_az_deg", *tel.MountAzDeg)
		}
		if tel.MQTTTaskReceiveMS != nil {
			pipe.HSet(ctx, stateKey, "mqtt_task_receive_ms", *tel.MQTTTaskReceiveMS)
		}
		if tel.Status == "idle" && prevStatus != "" && prevStatus != "idle" {
			pipe.HSet(ctx, stateKey, "idle_since", time.Now().UTC().Format(time.RFC3339))
		} else if tel.Status != "idle" && prevStatus == "idle" {
			pipe.HDel(ctx, stateKey, "idle_since")
		}
		_, err := pipe.Exec(ctx)
		if err != nil {
			sugar.Warnw("redis pipeline", "err", err)
		}
		t.Step("redis_pipeline_exec")

		// Forward vitals as an Observation (#25, #370) — throttled on-change or
		// 60s heartbeat so decision-critical fields never flood MQTT/CPU, while
		// still surfacing all 34 edge wait vitals once wired. Fail-open.
		if obs := maybeBuildEdgeObservation(edgeThrottle, tel, nodeStatus, estStartup, time.Now().UTC()); obs != nil {
			if err := hpmetrics.PublishObservation(c, tel.NodeID, *obs); err != nil {
				sugar.Warnw("metrics telemetry publish failed", "err", err, "node_id", tel.NodeID)
			}
		}

		t.Report(sugar, 0, tel.NodeID)
	})
	if !waitForSubscription(token, sugar, "telemetry") {
		return
	}
}

func estimateStartupMS(tel shared.Telemetry) int {
	switch tel.Status {
	case "idle":
		return 100
	case "observing":
		return 800
	case "uploading":
		return 500
	case "error":
		return 5000
	default:
		return 1000
	}
}

func telemetryNodeStatus(status string) (string, bool) {
	switch status {
	case "idle":
		return shared.NodeStatusIdle, true
	case "slewing", "observing", "uploading", "processing":
		return shared.NodeStatusBusy, true
	case "error":
		return "error", true
	default:
		return "", false
	}
}
