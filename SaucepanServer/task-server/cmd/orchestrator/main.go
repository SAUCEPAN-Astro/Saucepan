package main

import (
	"context"
	"encoding/json"
	"os"
	"os/signal"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/go-redis/redis/v8"
	pgx "github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/saucepan/hotpath/shared"
	"go.uber.org/zap"
)

func main() {
	zapLogger, _ := zap.NewProduction()
	defer zapLogger.Sync()
	sugar := zapLogger.Sugar()

	pgDSN := os.Getenv("PG_DSN")
	if pgDSN == "" {
		if os.Getenv("DEV_MODE") != "1" {
			sugar.Fatal("PG_DSN is required (set PG_DSN, or DEV_MODE=1 to use the localhost dev default)")
		}
		pgDSN = "postgres://postgres:password@localhost:5432/server_db"
		sugar.Warn("PG_DSN unset — using localhost dev default because DEV_MODE=1")
	}

	leaseCfg = leaseConfigFromEnv()

	preemptThreshold := envInt("PREEMPT_PRIORITY_THRESHOLD", shared.PreemptThresholdDefault)
	slewNearbyMs := envInt("SLEW_NEARBY_THRESHOLD_MS", shared.SlewNearbyThresholdMsDefault)
	metricsFlushInterval := envInt("METRICS_FLUSH_INTERVAL_MS", 5000)
	cleanStaleOnStartup := envBool("CLEAN_STALE_ON_STARTUP", true)

	tStart := shared.NewTimer("orchestrator_startup")
	ctx := context.Background()

	redisOpt, err := shared.RedisOptionsFromEnv()
	if err != nil {
		sugar.Fatalf("redis config: %v", err)
	}
	rdb := redis.NewClient(redisOpt)
	if err := rdb.Ping(ctx).Err(); err != nil {
		sugar.Fatalf("redis connect: %v", err)
	}
	tStart.Step("redis_connected")
	sugar.Info("orchestrator connected to redis")

	if cleanStaleOnStartup {
		pipe := rdb.Pipeline()
		pipe.Del(ctx, shared.RedisActiveTasks) // legacy key
		pipe.Del(ctx, shared.RedisQueuedTasks)
		pipe.Del(ctx, shared.RedisQueuedPlanned)
		pipe.Del(ctx, shared.RedisInflightTasks)
		if _, err := pipe.Exec(ctx); err != nil {
			sugar.Warnw("stale cleanup failed", "err", err)
		} else {
			sugar.Info("stale tasks:queued/tasks:queued:planned/tasks:inflight/tasks:active cleared on startup")
		}
	}

	metrics := shared.NewMetricsCollector(rdb, sugar, time.Duration(metricsFlushInterval)*time.Millisecond)
	defer metrics.Stop()
	tStart.Step("metrics_initialized")

	opts, err := shared.MQTTClientOptionsFromEnv("tcp://localhost:1883", "orchestrator")
	if err != nil {
		sugar.Fatalf("mqtt config: %v", err)
	}
	mqttClient := mqtt.NewClient(opts)
	for i := 0; i < 30; i++ {
		if token := mqttClient.Connect(); token.Wait() && token.Error() != nil {
			sugar.Warnw("mqtt connect attempt failed, retrying...", "attempt", i+1, "err", token.Error())
			time.Sleep(1 * time.Second)
			continue
		}
		break
	}
	if !mqttClient.IsConnected() {
		sugar.Fatal("mqtt connect failed after 30 retries")
	}
	metrics.SetMQTTPublisher(mqttClient)
	tStart.Step("mqtt_connected")
	sugar.Info("orchestrator connected to mqtt")

	pool, err := connectPostgres(ctx, pgDSN, sugar)
	if err != nil {
		sugar.Fatal(err)
	}
	defer pool.Close()

	listenPoolConn, pgListenConn, err := setupPGListen(ctx, pool, sugar)
	if err != nil {
		sugar.Fatal(err)
	}
	defer listenPoolConn.Release()
	tStart.Step("pg_listening")

	warmCache(ctx, rdb, pool, sugar)
	recovered := recoverPendingTasks(ctx, pool, rdb, sugar)
	tStart.Step("tasks_recovered")
	sugar.Infow("recovered pending tasks", "count", recovered)
	tStart.Report(sugar, 0, "")

	sugar.Info("waiting for active nodes from collector...")
	for i := 0; ; i++ {
		cnt, err := rdb.SCard(ctx, shared.RedisActiveNodes).Result()
		if err == nil && cnt > 0 {
			sugar.Infow("active nodes detected", "count", cnt, "waited_attempts", i)
			break
		}
		if i >= 120 {
			sugar.Warn("no active nodes after 60s — continuing anyway")
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	sugar.Info("orchestrator ready")

	go drainLoop(ctx, pool, rdb, mqttClient, metrics, sugar, preemptThreshold, slewNearbyMs)
	go reclaimLoop(ctx, pool, rdb, metrics, sugar)
	startPlannerLoop(ctx, pool, rdb, sugar)
	startCoverageLoops(ctx, pool, rdb, sugar)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	runNotifyLoop(ctx, pool, pgListenConn, rdb, mqttClient, metrics, sugar, sig, preemptThreshold, slewNearbyMs)
}

func runNotifyLoop(
	ctx context.Context,
	pool *pgxpool.Pool,
	pgListenConn *pgx.Conn,
	rdb *redis.Client,
	mqttClient mqtt.Client,
	metrics *shared.MetricsCollector,
	sugar *zap.SugaredLogger,
	sig chan os.Signal,
	preemptThreshold int,
	slewNearbyMs int,
) {
	for {
		select {
		case <-sig:
			sugar.Info("shutting down")
			return
		default:
			notification, err := pgListenConn.WaitForNotification(ctx)
			if err != nil {
				sugar.Warnw("pg notify error", "err", err)
				time.Sleep(500 * time.Millisecond)
				continue
			}

			var payload shared.NotifyPayload
			if err := json.Unmarshal([]byte(notification.Payload), &payload); err != nil {
				sugar.Warnw("bad notify payload", "payload_bytes", len(notification.Payload), "err", err)
				continue
			}

			handleTaskNotification(ctx, pool, payload, rdb, mqttClient, metrics, sugar, preemptThreshold, slewNearbyMs)
		}
	}
}
