package main

import (
	"fmt"
	"sync"
	"time"

	hpmetrics "github.com/saucepan/hotpath/internal/metrics"
	"github.com/saucepan/hotpath/shared"
)

// Observation publish throttle: on-change of decision-critical fields OR 60s heartbeat.
// Redis node_state updates stay on every telemetry message (assign freshness).
const edgeObservationHeartbeat = 60 * time.Second

type edgeObsThrottle struct {
	mu       sync.Mutex
	lastFP   map[string]string
	lastSent map[string]time.Time
}

func newEdgeObsThrottle() *edgeObsThrottle {
	return &edgeObsThrottle{
		lastFP:   map[string]string{},
		lastSent: map[string]time.Time{},
	}
}

func (t *edgeObsThrottle) shouldPublish(nodeID, fingerprint string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	prevFP, hasFP := t.lastFP[nodeID]
	prevAt, hasAt := t.lastSent[nodeID]
	changed := !hasFP || prevFP != fingerprint
	heartbeat := !hasAt || now.Sub(prevAt) >= edgeObservationHeartbeat
	if !changed && !heartbeat {
		return false
	}
	t.lastFP[nodeID] = fingerprint
	t.lastSent[nodeID] = now
	return true
}

func edgeDecisionFingerprint(tel shared.Telemetry, nodeStatus string, estStartup int) string {
	task := ""
	if tel.CurrentTaskID != nil {
		task = fmt.Sprintf("%d", *tel.CurrentTaskID)
	}
	err := ""
	if tel.PyLastError != nil {
		err = *tel.PyLastError
	}
	return fmt.Sprintf("%s|%s|%s|%d|%s|%.0f",
		nodeStatus, tel.Status, task, estStartup, err, tel.LoadPct)
}

func buildEdgeOpsMetrics(tel shared.Telemetry, nodeStatus string, estStartup int, now time.Time) map[string]interface{} {
	ops := map[string]interface{}{
		"ops.node_status":     nodeStatus,
		"ops.telem_status":    tel.Status,
		"ops.load_pct":        tel.LoadPct,
		"ops.completed_files": tel.CompletedFiles,
		"ops.memory_avail_mb": tel.MemoryAvailMB,
		"ops.last_seen":       now.UTC().Format(time.RFC3339),
		"ops.node_id":         tel.NodeID,
		"ops.est_startup_ms":  estStartup,
		"ops.scope_heartbeat": 1,
	}
	if tel.CurrentTaskID != nil {
		ops["ops.current_task_id"] = *tel.CurrentTaskID
	}
	if tel.CurrentTaskPriority != nil {
		ops["ops.current_task_priority"] = *tel.CurrentTaskPriority
	}
	if tel.MountAltDeg != nil {
		ops["ops.mount_alt"] = *tel.MountAltDeg
	}
	if tel.MountAzDeg != nil {
		ops["ops.mount_az"] = *tel.MountAzDeg
	}
	if tel.MQTTTaskReceiveMS != nil {
		ops["ops.mqtt_task_receive_ms"] = *tel.MQTTTaskReceiveMS
		ops["ops.command_ack_latency_ms"] = *tel.MQTTTaskReceiveMS
	}
	setF := func(key string, v *float64) {
		if v != nil {
			ops[key] = *v
		}
	}
	setB := func(key string, v *bool) {
		if v != nil {
			ops[key] = *v
		}
	}
	setU64 := func(key string, v *uint64) {
		if v != nil {
			ops[key] = *v
		}
	}
	setI := func(key string, v *int) {
		if v != nil {
			ops[key] = *v
		}
	}
	setS := func(key string, v *string) {
		if v != nil {
			ops[key] = *v
		}
	}

	setF("ops.cpu_pct", tel.CPUPct)
	setF("ops.mem_pct", tel.MemPct)
	setF("ops.disk_pct", tel.DiskPct)
	setF("ops.disk_free_gb", tel.DiskFreeGB)
	setF("ops.net_tx", tel.NetTx)
	setF("ops.net_rx", tel.NetRx)
	setF("ops.host_temp", tel.HostTemp)
	setB("ops.alpaca_tele_conn", tel.AlpacaTeleConn)
	setB("ops.alpaca_cam_conn", tel.AlpacaCamConn)
	setF("ops.cam_temp", tel.CamTemp)
	if tel.FilterPos != nil {
		ops["ops.filter_pos"] = *tel.FilterPos
	}
	if tel.FocuserPos != nil {
		ops["ops.focuser_pos"] = *tel.FocuserPos
	}
	setB("ops.py_proc_running", tel.PyProcRunning)
	setU64("ops.py_images_processed", tel.PyImagesProcessed)
	setU64("ops.py_stacks_created", tel.PyStacksCreated)
	setU64("ops.py_uploads_done", tel.PyUploadsDone)
	setS("ops.py_last_error", tel.PyLastError)
	setF("ops.telem_location_lat", tel.LocationLat)
	setF("ops.telem_location_lon", tel.LocationLon)
	setI("ops.telem_status_http", tel.TelemStatusHTTP)
	setI("ops.telem_file_count", tel.TelemFileCount)
	setS("ops.telem_last_file_at", tel.TelemLastFileAt)
	setS("ops.telem_last_update_at", tel.TelemLastUpdateAt)
	if tel.TelemTaskID != nil {
		ops["ops.telem_task_id"] = *tel.TelemTaskID
	}
	setI("ops.telem_ttl_sec", tel.TelemTTLSec)
	setU64("ops.capture_duration_ms", tel.CaptureDurationMS)
	setU64("ops.plate_solve_ms", tel.PlateSolveMS)
	setU64("ops.local_reduce_ms", tel.LocalReduceMS)
	setU64("ops.upload_bytes", tel.UploadBytes)
	setU64("ops.upload_duration_ms", tel.UploadDurationMS)
	setU64("ops.upload_retries", tel.UploadRetries)
	setU64("ops.mqtt_reconnects", tel.MQTTReconnects)

	// Fallbacks from core fields when extended telem_* omitted.
	if _, ok := ops["ops.telem_file_count"]; !ok {
		ops["ops.telem_file_count"] = tel.CompletedFiles
	}
	if _, ok := ops["ops.telem_last_update_at"]; !ok {
		ops["ops.telem_last_update_at"] = now.UTC().Format(time.RFC3339)
	}
	if _, ok := ops["ops.cpu_pct"]; !ok {
		ops["ops.cpu_pct"] = tel.LoadPct
	}
	return ops
}

func maybeBuildEdgeObservation(
	throttle *edgeObsThrottle,
	tel shared.Telemetry,
	nodeStatus string,
	estStartup int,
	now time.Time,
) *hpmetrics.Observation {
	fp := edgeDecisionFingerprint(tel, nodeStatus, estStartup)
	if !throttle.shouldPublish(tel.NodeID, fp, now) {
		return nil
	}
	ops := buildEdgeOpsMetrics(tel, nodeStatus, estStartup, now)
	obs := hpmetrics.NewObservation(
		"edge_telemetry",
		"ops",
		tel.NodeID,
		ops,
		map[string]interface{}{"node_id": tel.NodeID, "telescope_id": tel.NodeID},
	)
	return &obs
}
