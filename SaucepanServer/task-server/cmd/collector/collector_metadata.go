package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/eclipse/paho.mqtt.golang"
	"github.com/go-redis/redis/v8"
	"github.com/saucepan/hotpath/shared"
	"go.uber.org/zap"
)

// startMetadataSubscriber sets up the /metadata/+ MQTT subscription handler.
func startMetadataSubscriber(ctx context.Context, client mqtt.Client, rdb *redis.Client, sugar *zap.SugaredLogger) {
	token := client.Subscribe(shared.SubscribeFilter(shared.TopicMetadata), 1, func(c mqtt.Client, msg mqtt.Message) {
		t := shared.NewTimer("metadata_ingest")
		var meta shared.NodeMetadata
		if err := json.Unmarshal(msg.Payload(), &meta); err != nil {
			sugar.Warnw("bad metadata", "err", err)
			return
		}
		nodeID, ok := bindTopicNodeID(msg.Topic(), shared.TopicPrefix(shared.TopicMetadata), meta.NodeID)
		if !ok {
			sugar.Warnw(formatReject(msg.Topic(), "topic/json node_id mismatch"),
				"json_node_id", meta.NodeID)
			return
		}
		meta.NodeID = nodeID
		t.Step("json_unmarshal")
		key := fmt.Sprintf(shared.RedisNodeMeta, meta.NodeID)
		// Reject sudden site teleports (spoofed reliability routing) — #244.
		if meta.SiteLat != nil && meta.SiteLon != nil {
			prevLat, errLat := rdb.HGet(ctx, key, "site_lat").Float64()
			prevLon, errLon := rdb.HGet(ctx, key, "site_lon").Float64()
			if errLat == nil && errLon == nil {
				if siteCoordJumpTooLarge(prevLat, prevLon, *meta.SiteLat, *meta.SiteLon, 5.0) {
					sugar.Warnw(formatReject(msg.Topic(), "site coordinate jump too large"),
						"node_id", meta.NodeID,
						"prev_lat", prevLat, "prev_lon", prevLon,
						"next_lat", *meta.SiteLat, "next_lon", *meta.SiteLon,
					)
					return
				}
			}
		}
		pipe := rdb.Pipeline()
		pipe.HSet(ctx, key, "node_id", meta.NodeID)
		pipe.HSet(ctx, key, "hardware_specs", meta.HardwareSpecs)
		pipe.HSet(ctx, key, "quality_tier", meta.QualityTier)
		pipe.HSet(ctx, key, "reliability_score", meta.ReliabilityScore)
		pipe.HSet(ctx, key, "power", meta.Power)
		if meta.ApertureMM != nil {
			pipe.HSet(ctx, key, "aperture_mm", *meta.ApertureMM)
		}
		if meta.FocalLengthMM != nil {
			pipe.HSet(ctx, key, "focal_length_mm", *meta.FocalLengthMM)
		}
		if meta.PixelSizeUm != nil {
			pipe.HSet(ctx, key, "pixel_size_um", *meta.PixelSizeUm)
		}
		if meta.SiteLat != nil {
			pipe.HSet(ctx, key, "site_lat", *meta.SiteLat)
		}
		if meta.SiteLon != nil {
			pipe.HSet(ctx, key, "site_lon", *meta.SiteLon)
		}
		if meta.MountSlewRateDegS != nil {
			pipe.HSet(ctx, key, "mount_slew_rate_deg_s", *meta.MountSlewRateDegS)
		}
		if meta.FOVWidthArcmin != nil {
			pipe.HSet(ctx, key, "fov_width_arcmin", *meta.FOVWidthArcmin)
		}
		if meta.FOVHeightArcmin != nil {
			pipe.HSet(ctx, key, "fov_height_arcmin", *meta.FOVHeightArcmin)
		}
		if meta.MountType != nil {
			pipe.HSet(ctx, key, "mount_type", *meta.MountType)
		}
		if meta.MaxStableExposureS != nil {
			pipe.HSet(ctx, key, "max_stable_exposure_s", *meta.MaxStableExposureS)
		}
		if meta.SiteSeeingArcsec != nil {
			pipe.HSet(ctx, key, "median_seeing_arcsec", *meta.SiteSeeingArcsec)
		}
		if meta.LimitingMagnitude != nil {
			pipe.HSet(ctx, key, "limiting_magnitude", *meta.LimitingMagnitude)
		}
		if len(meta.AvailableFilters) > 0 {
			filterJSON, _ := json.Marshal(meta.AvailableFilters)
			pipe.HSet(ctx, key, "available_filters", string(filterJSON))
		}
		if len(meta.ObstructionMask) > 0 {
			maskJSON, _ := json.Marshal(meta.ObstructionMask)
			pipe.HSet(ctx, key, "obstruction_mask", string(maskJSON))
		}
		if meta.MountLimits != nil {
			limitsJSON, _ := json.Marshal(meta.MountLimits)
			pipe.HSet(ctx, key, "mount_limits", string(limitsJSON))
		}
		if meta.HorizonProfile != nil {
			horizonJSON, _ := json.Marshal(meta.HorizonProfile)
			pipe.HSet(ctx, key, "horizon_profile", string(horizonJSON))
		}
		if len(meta.EnabledCampaignIDs) > 0 {
			idsJSON, _ := json.Marshal(meta.EnabledCampaignIDs)
			pipe.HSet(ctx, key, "enabled_campaign_ids", string(idsJSON))
		}
		if strings.HasPrefix(meta.NodeID, "emu_") {
			pipe.HSet(ctx, key, "is_emulator", true)
		}
		anomalyMode := meta.AnomalyMode
		if anomalyMode == "" {
			anomalyMode = "off"
		}
		pipe.HSet(ctx, key, "anomaly_mode", anomalyMode)
		pipe.HSet(ctx, key, "allow_anomaly_retarget", meta.AllowAnomalyRetarget)
		pipe.Expire(ctx, key, 24*time.Hour)
		_, err := pipe.Exec(ctx)
		if err != nil {
			sugar.Warnw("redis metadata pipeline", "err", err)
		}
		t.Step("redis_pipeline_exec")
		t.Report(sugar, 0, meta.NodeID)
	})
	if !waitForSubscription(token, sugar, "metadata") {
		return
	}
}
