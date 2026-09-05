package wire

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"
)

// TelemetryHeartbeatMax is the publish-cadence floor for /telemetry/{node_id}
// (QoS 0, not retained, "on-change or 60s" per collector convention). Used by
// cmd/saucepan to decide when telemetry silence means offline rather than
// waiting. See docs/design/PIER_CLI.md §5.
const TelemetryHeartbeatMax = 60 * time.Second

// Telemetry from edge node (JSON over MQTT /telemetry/{node_id}).
// Moved verbatim from shared/models.go:80-126 (#457).
type Telemetry struct {
	NodeID              string   `json:"node_id"`
	Status              string   `json:"status"`   // idle, slewing, observing, uploading, error
	LoadPct             float64  `json:"load_pct"` // 0-100
	CurrentTaskID       *int     `json:"current_task_id,omitempty"`
	CurrentTaskPriority *int     `json:"current_task_priority,omitempty"`
	MountAltDeg         *float64 `json:"mount_alt_deg,omitempty"`
	MountAzDeg          *float64 `json:"mount_az_deg,omitempty"`
	CompletedFiles      int      `json:"completed_files"`
	MemoryAvailMB       float64  `json:"memory_avail_mb"`
	// Wall time from assign command sent_at to client MQTT receive (edge-measured).
	MQTTTaskReceiveMS *float64 `json:"mqtt_task_receive_ms,omitempty"`

	// Optional host / instrument ops fields (edge may omit).
	CPUPct            *float64 `json:"cpu_pct,omitempty"`
	MemPct            *float64 `json:"mem_pct,omitempty"`
	DiskPct           *float64 `json:"disk_pct,omitempty"`
	DiskFreeGB        *float64 `json:"disk_free_gb,omitempty"`
	NetTx             *float64 `json:"net_tx,omitempty"`
	NetRx             *float64 `json:"net_rx,omitempty"`
	HostTemp          *float64 `json:"host_temp,omitempty"`
	AlpacaTeleConn    *bool    `json:"alpaca_tele_conn,omitempty"`
	AlpacaCamConn     *bool    `json:"alpaca_cam_conn,omitempty"`
	CamTemp           *float64 `json:"cam_temp,omitempty"`
	FilterPos         *int     `json:"filter_pos,omitempty"`
	FocuserPos        *int     `json:"focuser_pos,omitempty"`
	PyProcRunning     *bool    `json:"py_proc_running,omitempty"`
	PyImagesProcessed *uint64  `json:"py_images_processed,omitempty"`
	PyStacksCreated   *uint64  `json:"py_stacks_created,omitempty"`
	PyUploadsDone     *uint64  `json:"py_uploads_done,omitempty"`
	PyLastError       *string  `json:"py_last_error,omitempty"`
	LocationLat       *float64 `json:"location_lat,omitempty"`
	LocationLon       *float64 `json:"location_lon,omitempty"`
	TelemStatusHTTP   *int     `json:"telem_status_http,omitempty"`
	TelemFileCount    *int     `json:"telem_file_count,omitempty"`
	TelemLastFileAt   *string  `json:"telem_last_file_at,omitempty"`
	TelemLastUpdateAt *string  `json:"telem_last_update_at,omitempty"`
	TelemTaskID       *int     `json:"telem_task_id,omitempty"`
	TelemTTLSec       *int     `json:"telem_ttl_sec,omitempty"`
	CaptureDurationMS *uint64  `json:"capture_duration_ms,omitempty"`
	PlateSolveMS      *uint64  `json:"plate_solve_ms,omitempty"`
	LocalReduceMS     *uint64  `json:"local_reduce_ms,omitempty"`
	UploadBytes       *uint64  `json:"upload_bytes,omitempty"`
	UploadDurationMS  *uint64  `json:"upload_duration_ms,omitempty"`
	UploadRetries     *uint64  `json:"upload_retries,omitempty"`
	MQTTReconnects    *uint64  `json:"mqtt_reconnects,omitempty"`
}

// NodeMetadata published on boot (MQTT /metadata/{node_id}), retained.
// Moved verbatim from shared/models.go:129-153 (#457).
type NodeMetadata struct {
	NodeID               string          `json:"node_id"`
	HardwareSpecs        string          `json:"hardware_specs"` // free-text
	QualityTier          string          `json:"quality_tier"`   // premium, standard, community
	AvailableFilters     []string        `json:"available_filters"`
	Power                float64         `json:"power"` // 0.0-1.0
	ApertureMM           *float64        `json:"aperture_mm,omitempty"`
	FocalLengthMM        *float64        `json:"focal_length_mm,omitempty"`
	PixelSizeUm          *float64        `json:"pixel_size_um,omitempty"`
	SiteLat              *float64        `json:"site_lat,omitempty"`
	SiteLon              *float64        `json:"site_lon,omitempty"`
	ReliabilityScore     float64         `json:"reliability_score"`               // 0.0-1.0
	MountSlewRateDegS    *float64        `json:"mount_slew_rate_deg_s,omitempty"` // degrees/second
	ObstructionMask      ObstructionMask `json:"obstruction_mask,omitempty"`      // forbidden polygons
	MountLimits          *MountLimits    `json:"mount_limits,omitempty"`
	HorizonProfile       *HorizonProfile `json:"horizon_profile,omitempty"`
	FOVWidthArcmin       *float64        `json:"fov_width_arcmin,omitempty"`
	FOVHeightArcmin      *float64        `json:"fov_height_arcmin,omitempty"`
	MountType            *int            `json:"mount_type,omitempty"`
	MaxStableExposureS   *float64        `json:"max_stable_exposure_s,omitempty"`
	SiteSeeingArcsec     *float64        `json:"median_seeing_arcsec,omitempty"`
	LimitingMagnitude    *float64        `json:"limiting_magnitude,omitempty"`
	EnabledCampaignIDs   []string        `json:"enabled_campaign_ids,omitempty"`
	AnomalyMode          string          `json:"anomaly_mode,omitempty"`
	AllowAnomalyRetarget bool            `json:"allow_anomaly_retarget,omitempty"`
}

// Command from server to edge node (MQTT /commands/{node_id}).
// Sig is hex(HMAC-SHA256) over canonical v1 binding (#241).
// Moved verbatim from shared/models.go:160-166 (#457).
type Command struct {
	Type    string      `json:"type"` // assign_task, preempt_task, abort_task, ping
	NodeID  string      `json:"node_id,omitempty"`
	Payload interface{} `json:"payload"`
	SentAt  string      `json:"sent_at"` // ISO 8601
	Sig     string      `json:"sig,omitempty"`
}

// Moved verbatim from shared/models.go:168-180 (#457).
type AssignTaskPayload struct {
	TaskID          int      `json:"task_id"`
	CampaignID      string   `json:"campaign_id,omitempty"`
	Priority        int      `json:"priority"`
	Name            string   `json:"name"`
	IntegrationTime float64  `json:"integration_time"`
	RequiredFilters []string `json:"required_filters"`
	TargetRA        *float64 `json:"target_ra,omitempty"`
	TargetDec       *float64 `json:"target_dec,omitempty"`
	MinAltitudeDeg  *float64 `json:"min_altitude_deg,omitempty"`
	HandoffUrgency  string   `json:"handoff_urgency,omitempty"`
	ScheduledEndAt  *string  `json:"scheduled_end_at,omitempty"`
	// PierCodeGrants is the resolved on-pier-code capability map for this
	// campaign (#470 step 3 / #516): action name → allowed. Absent unless the
	// campaign's pack enables `pier_code`; a pier that receives it still runs
	// nothing without a local consent record (#517) and an unset kill switch
	// (#520). omitempty keeps it off every ordinary assign.
	PierCodeGrants map[string]bool `json:"pier_code_grants,omitempty"`
	// PierCode is the reference to the researcher artifact to run for this
	// campaign (#470 step 5 / #518) — content hash + fetch URL. The pier
	// fetches it, verifies the hash, caches it by hash (re-assign of the same
	// hash does not re-fetch), and runs it only with consent + kill switch
	// clear. Absent = no code to run. omitempty.
	PierCode *PierCodeRef `json:"pier_code,omitempty"`
	// PierCodeDisabled is the campaign kill switch (#470 step 7 / #520). When
	// true the pier skips running or continuing this campaign's code.
	PierCodeDisabled bool `json:"pier_code_disabled,omitempty"`
}

// Moved verbatim from shared/models.go:182-185 (#457).
type PreemptTaskPayload struct {
	PrevTaskID int               `json:"prev_task_id"`
	NewTask    AssignTaskPayload `json:"new_task"`
}

// ObstructionMask is a list of forbidden-altitude polygons. Each polygon is
// a list of [altDeg, azDeg] pairs. Type declaration moved from
// shared/obstruction.go:17 (#457); the geometry functions (PointInPolygon,
// PointInForbiddenAltAz, SlewPathHitsForbidden) stay in shared/obstruction.go
// — they are scheduler logic, not wire contract.
type ObstructionMask [][][]float64

// MountLimits matches client mount_limits JSON. Type declaration moved from
// shared/safety_match.go:10-19 (#457); ValidateMountLimits stays in
// shared/safety_match.go.
type MountLimits struct {
	Altitude struct {
		Min *float64 `json:"min,omitempty"`
		Max *float64 `json:"max,omitempty"`
	} `json:"altitude,omitempty"`
	Azimuth struct {
		Min *float64 `json:"min,omitempty"`
		Max *float64 `json:"max,omitempty"`
	} `json:"azimuth,omitempty"`
}

// HorizonProfile matches client horizon_profile JSON. Type declaration
// moved from shared/safety_match.go:22-28 (#457); AboveHorizonProfile stays
// in shared/safety_match.go.
type HorizonProfile struct {
	Points []struct {
		Az  float64 `json:"az"`
		Alt float64 `json:"alt"`
	} `json:"points"`
	Interpolation string `json:"interpolation,omitempty"`
}

// NodeStatus is the payload on /status/{node_id}. New (#457) — the Rust
// client and Go server both build this shape ad hoc; the CLI is the first
// Go reader.
type NodeStatus struct {
	NodeID string `json:"node_id"`
	Status string `json:"status"`
}

// BoardNote is the opaque message envelope on a /board/ topic. Message is an
// arbitrary researcher-defined string; the transport does not interpret it.
// Live subscribers receive every publish. The topic remains retained so a
// late subscriber also gets the sender's latest message as a starting point;
// the durable campaign history is maintained separately by the collector
// bridge. MessageID lets receivers distinguish a retained replay from a
// repeated live delivery.
//
// Exactly one of TaskID / CampaignID is set, matching the topic the note
// rides: TaskID on /board/{task_id}/{node_id} (per-task, #463), CampaignID
// on /board/campaign/{campaign_id}/{node_id} (campaign-wide cross-task
// coordination, #470 step 8).
type BoardNote struct {
	TaskID     string `json:"task_id,omitempty"`
	CampaignID string `json:"campaign_id,omitempty"`
	NodeID     string `json:"node_id"`
	MessageID  string `json:"message_id,omitempty"`
	Message    string `json:"message"`
	// EventType and Payload are compatibility metadata for existing typed
	// on-pier actions. The live board never assigns meaning to them; researcher
	// code may ignore them and interpret Message however it wants.
	EventType string          `json:"event_type,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	SentAt    time.Time       `json:"sent_at"`
}

// NewMessageID creates an opaque identifier for a board message. It is
// transport metadata only; researcher code is free to ignore it.
func NewMessageID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err == nil {
		return hex.EncodeToString(b)
	}
	return time.Now().UTC().Format(time.RFC3339Nano)
}

// SameBoardMessage reports whether two envelopes identify the same transport
// message, preferring MessageID and falling back to the legacy envelope fields.
func SameBoardMessage(a, b BoardNote) bool {
	if a.MessageID != "" || b.MessageID != "" {
		return a.MessageID != "" && a.MessageID == b.MessageID
	}
	return a.NodeID == b.NodeID && a.SentAt.Equal(b.SentAt) && a.Message == b.Message &&
		a.EventType == b.EventType && bytes.Equal(a.Payload, b.Payload)
}
