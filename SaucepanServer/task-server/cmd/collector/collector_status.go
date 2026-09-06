package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/eclipse/paho.mqtt.golang"
	"github.com/go-redis/redis/v8"
	"github.com/saucepan/hotpath/shared"
	"go.uber.org/zap"
)

// startStatusSubscriber sets up the /status/+ MQTT subscription handler.
func startStatusSubscriber(ctx context.Context, client mqtt.Client, rdb *redis.Client, sugar *zap.SugaredLogger) {
	token := client.Subscribe(shared.SubscribeFilter(shared.TopicStatus), 1, func(c mqtt.Client, msg mqtt.Message) {
		var status struct {
			NodeID string `json:"node_id"`
			Status string `json:"status"`
		}
		if err := json.Unmarshal(msg.Payload(), &status); err != nil {
			return
		}
		nodeID, ok := bindTopicNodeID(msg.Topic(), shared.TopicPrefix(shared.TopicStatus), status.NodeID)
		if !ok {
			sugar.Warnw(formatReject(msg.Topic(), "topic/json node_id mismatch"),
				"json_node_id", status.NodeID)
			return
		}
		status.NodeID = nodeID
		stateKey := fmt.Sprintf(shared.RedisNodeState, status.NodeID)
		if status.Status == shared.NodeStatusOffline {
			start := time.Now()
			rdb.HSet(ctx, stateKey, "status", "offline", "last_seen", time.Now().UTC().Format(time.RFC3339))
			rdb.Expire(ctx, stateKey, 30*time.Second)
			rdb.SRem(ctx, shared.RedisActiveNodes, status.NodeID)
			sugar.Infow("TIMING lwt_mark_offline",
				"node_id", status.NodeID,
				"us", time.Since(start).Microseconds(),
			)
			sugar.Warnw("node offline", "node", status.NodeID)
		} else if status.Status == shared.NodeStatusOnline {
			rdb.HSet(ctx, stateKey, "status", shared.NodeStatusIdle, "last_seen", time.Now().UTC().Format(time.RFC3339))
			rdb.SAdd(ctx, shared.RedisActiveNodes, status.NodeID)
			rdb.Expire(ctx, stateKey, shared.StateTTLSeconds*time.Second)
			sugar.Infow("node online", "node", status.NodeID)
		}
	})
	if !waitForSubscription(token, sugar, "status") {
		return
	}
}
