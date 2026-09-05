package main

import (
	"sync"
	"time"
)

// Ephemeral HTTP-era cache — kept for handoff helpers until Redis-backed reads land.
// MQTT collector is the source of truth for live node state.

type telemetryLocation struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type telemetrySnapshot struct {
	TelescopeID         string             `json:"telescope_id"`
	Location            *telemetryLocation `json:"location,omitempty"`
	Status              string             `json:"status,omitempty"`
	FileCount           int                `json:"file_count"`
	TaskID              *int64             `json:"task_id,omitempty"`
	MountAltDeg         *float64           `json:"mount_alt_deg,omitempty"`
	MountAzDeg          *float64           `json:"mount_az_deg,omitempty"`
	ObstructionMaskLive []byte             `json:"-"`
	LastUpdateAt        time.Time          `json:"last_update_at"`
	LastFileCompletedAt *time.Time         `json:"last_file_completed_at,omitempty"`
}

var (
	telemetryMu    sync.RWMutex
	telemetryCache = map[string]telemetrySnapshot{}
)

func getTelemetrySnapshot(telescopeID string) (telemetrySnapshot, bool) {
	telemetryMu.RLock()
	defer telemetryMu.RUnlock()
	snap, ok := telemetryCache[telescopeID]
	return snap, ok
}
