package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/eclipse/paho.mqtt.golang"
	"github.com/go-redis/redis/v8"
	"github.com/saucepan/hotpath/shared"
	"go.uber.org/zap"
)

func waitForSubscription(token mqtt.Token, log *zap.SugaredLogger, name string) bool {
	if !token.WaitTimeout(5 * time.Second) {
		log.Warnw("mqtt subscription timed out", "subscription", name)
		return false
	}
	if err := token.Error(); err != nil {
		log.Warnw("mqtt subscription failed", "subscription", name, "err", err)
		return false
	}
	return true
}

func main() {
	zapLogger, _ := zap.NewProduction()
	defer zapLogger.Sync()
	sugar := zapLogger.Sugar()

	tMain := shared.NewTimer("collector_startup")
	edgeThrottle := newEdgeObsThrottle()

	redisOpt, err := shared.RedisOptionsFromEnv()
	if err != nil {
		sugar.Fatalf("redis config: %v", err)
	}
	rdb := redis.NewClient(redisOpt)
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		sugar.Fatalf("redis connect: %v", err)
	}
	tMain.Step("redis_connected")
	sugar.Info("connected to redis")

	opts, err := shared.MQTTClientOptionsFromEnv("tcp://localhost:1883", "state-collector")
	if err != nil {
		sugar.Fatalf("mqtt config: %v", err)
	}
	client := mqtt.NewClient(opts)
	for i := 0; i < 30; i++ {
		if token := client.Connect(); token.Wait() && token.Error() != nil {
			sugar.Warnw("mqtt connect attempt failed, retrying...", "attempt", i+1, "err", token.Error())
			time.Sleep(1 * time.Second)
			continue
		}
		break
	}
	if !client.IsConnected() {
		sugar.Fatal("mqtt connect failed after 30 retries")
	}
	tMain.Step("mqtt_connected")
	tMain.Report(sugar, 0, "")
	sugar.Info("connected to mqtt")

	// Telemetry — /telemetry/{node_id}
	startTelemetrySubscriber(ctx, client, rdb, edgeThrottle, sugar)

	// Metadata — /metadata/{node_id}
	startMetadataSubscriber(ctx, client, rdb, sugar)

	// Status — /status/{node_id} (LWT + online)
	startStatusSubscriber(ctx, client, rdb, sugar)

	// Campaign board bridge: MQTT /board/campaign/+/+ → campaign_board_notes,
	// so on-pier code's board_post / alert / urgency / request_time reaches the
	// researcher's HTTP board (#470). Fail-open — off entirely without PG_DSN.
	startCampaignBoardBridge(ctx, client, sugar)

	// Lease renewal: telemetry still on a task → push its assignment lease
	// forward so the orchestrator's reclaim loop only fires on real node death
	// (#403). Fail-open — off entirely without PG_DSN.
	startLeaseRenewer(ctx, client, sugar)

	// Node-state reconcile: the *only* path by which the collector may clear
	// the orchestrator-owned current_task_* fields — idle telemetry that also
	// carries no task id, confirmed against a DB-terminal task row (#404).
	// Fail-open — off entirely without PG_DSN.
	startNodeStateReconciler(ctx, rdb, client, sugar)

	sugar.Info("state collector running")
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	sugar.Info("shutting down")
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
