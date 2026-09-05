package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/go-redis/redis/v8"

	"github.com/saucepan/hotpath/shared"
)

var redisClient *redis.Client

func initRedis() {
	if os.Getenv("REDIS_ADDR") == "" && os.Getenv("REDIS_URL") == "" {
		log.Println("REDIS_ADDR unset — telescope register will not sync orchestrator cache")
		return
	}
	opt, err := shared.RedisOptionsFromEnv()
	if err != nil {
		log.Fatalf("redis config: %v", err)
	}
	redisClient = redis.NewClient(opt)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := redisClient.Ping(ctx).Err(); err != nil {
		log.Printf("Redis unavailable (%s): %v — orchestrator cache sync disabled", opt.Addr, err)
		redisClient = nil
		return
	}
	log.Printf("Redis connected (%s) for telescope cache sync", opt.Addr)
}

// syncTelescopeRedisMeta pushes registration fields into the orchestrator node meta hash
// so matching gates see optics immediately (without waiting for MQTT or warmCache).
func syncTelescopeRedisMeta(t *TelescopeRegistration) {
	if redisClient == nil || t == nil || t.TelescopeID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	key := fmt.Sprintf(shared.RedisNodeMeta, t.TelescopeID)
	pipe := redisClient.Pipeline()
	pipe.HSet(ctx, key, "node_id", t.TelescopeID)
	if t.Power > 0 {
		pipe.HSet(ctx, key, "power", t.Power)
	}
	if len(t.AvailableFilters) > 0 {
		if b, err := json.Marshal(t.AvailableFilters); err == nil {
			pipe.HSet(ctx, key, "available_filters", string(b))
		}
	}
	if len(t.EnabledCampaignIDs) > 0 {
		if idsJSON, err := json.Marshal(t.EnabledCampaignIDs); err == nil {
			pipe.HSet(ctx, key, "enabled_campaign_ids", string(idsJSON))
		}
	}
	if t.IsEmulator {
		pipe.HSet(ctx, key, "is_emulator", true)
	}
	if t.ApertureMM > 0 {
		pipe.HSet(ctx, key, "aperture_mm", t.ApertureMM)
	}
	if t.FocalLengthMM > 0 {
		pipe.HSet(ctx, key, "focal_length_mm", t.FocalLengthMM)
	}
	if t.PixelSizeUM > 0 {
		pipe.HSet(ctx, key, "pixel_size_um", t.PixelSizeUM)
	}
	if t.SiteLatitude != nil {
		pipe.HSet(ctx, key, "site_lat", *t.SiteLatitude)
	}
	if t.SiteLongitude != nil {
		pipe.HSet(ctx, key, "site_lon", *t.SiteLongitude)
	}
	if t.SeeingArcsec > 0 {
		pipe.HSet(ctx, key, "median_seeing_arcsec", t.SeeingArcsec)
	}
	if t.LimitingMagnitude != nil {
		pipe.HSet(ctx, key, "limiting_magnitude", *t.LimitingMagnitude)
	}
	if t.FOVWidthArcmin > 0 {
		pipe.HSet(ctx, key, "fov_width_arcmin", t.FOVWidthArcmin)
	}
	if t.FOVHeightArcmin > 0 {
		pipe.HSet(ctx, key, "fov_height_arcmin", t.FOVHeightArcmin)
	}
	if t.MountType != 0 {
		pipe.HSet(ctx, key, "mount_type", t.MountType)
	}
	if t.MaxStableExposureS > 0 {
		pipe.HSet(ctx, key, "max_stable_exposure_s", t.MaxStableExposureS)
	}
	pipe.Expire(ctx, key, 24*time.Hour)
	if _, err := pipe.Exec(ctx); err != nil {
		log.Printf("redis telescope sync %s: %v", t.TelescopeID, err)
	}
}

// clearNodeAssignmentForTask drops the orchestrator-owned current_task_* marker
// from every node that held the given (now terminal) task, and puts the node's
// scheduling status back to idle. #404: task completion is a *defined* clear
// point for current_task_* — alongside the orchestrator's own preempt and
// lease-reclaim paths — whereas the collector never clears it on a bare idle
// heartbeat. Best-effort: the tasks row is the lifecycle source of truth; this
// only keeps the Redis hot-path cache from advertising a stale assignment.
func clearNodeAssignmentForTask(ctx context.Context, taskInternalID int) {
	if redisClient == nil || db == nil || taskInternalID <= 0 {
		return
	}
	rows, err := db.Query(ctx, `
		SELECT telescope_id FROM task_assignments WHERE task_id = $1
		UNION
		SELECT assigned_telescope_id FROM tasks
		WHERE id = $1 AND assigned_telescope_id IS NOT NULL AND TRIM(assigned_telescope_id) <> ''
	`, taskInternalID)
	if err != nil {
		log.Printf("clear node assignment for task %d: %v", taskInternalID, err)
		return
	}
	var nodeIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil && id != "" {
			nodeIDs = append(nodeIDs, id)
		}
	}
	rows.Close()
	if rows.Err() != nil {
		log.Printf("clear node assignment for task %d: scan: %v", taskInternalID, rows.Err())
	}

	want := strconv.Itoa(taskInternalID)
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	for _, nodeID := range nodeIDs {
		key := fmt.Sprintf(shared.RedisNodeState, nodeID)
		cur, err := redisClient.HGet(cctx, key, "current_task_id").Result()
		if err == redis.Nil {
			continue // node has no assignment marker
		}
		if err != nil {
			log.Printf("clear node assignment: HGet %s: %v", nodeID, err)
			continue
		}
		if cur != want {
			continue // node already moved on to a fresher task
		}
		pipe := redisClient.Pipeline()
		pipe.HDel(cctx, key, "current_task_id", "current_task_priority", "idle_since")
		pipe.HSet(cctx, key, "status", shared.NodeStatusIdle)
		if _, err := pipe.Exec(cctx); err != nil {
			log.Printf("clear node assignment: exec %s: %v", nodeID, err)
		}
	}
}
