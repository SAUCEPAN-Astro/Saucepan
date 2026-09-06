package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/saucepan/hotpath/shared"
	"github.com/saucepan/hotpath/shared/wire"
)

// Config is pier-agent's full runtime configuration: env vars for
// transport endpoints (matching the CLI's env-var convention, PIER_CLI.md
// §2) plus a local JSON file for the site/safety data an operator actually
// needs to set once (mount limits, horizon, obstructions) - the same
// shapes NodeMetadata already carries, so this file's contents are exactly
// what gets published as this node's metadata on connect.
type Config struct {
	NodeID           string
	AlpacaBaseURL    string
	TelescopeNum     int
	CameraNum        int
	FilterWheelNum   int
	CaptureDir       string
	TelemetryPeriod  time.Duration
	MQTTBroker       string
	SafetyConfigPath string
	APIURL           string
	DeviceToken      string
	UploadChunkSize  int

	// On-pier researcher code (#470). PierCodeRunnerPath empty = feature off:
	// the agent never forks a runner and never fetches an artifact.
	PierCodeRunnerPath string
	PierCodeCacheDir   string

	Safety SafetyConfig
}

// SafetyConfig mirrors the fields of wire.NodeMetadata that describe a
// site's physical safety envelope. Loaded from a local JSON file so an
// operator sets it once per install; pier-agent never derives it and never
// guesses a default that could let a real mount slew somewhere unsafe.
type SafetyConfig struct {
	SiteLat           *float64             `json:"site_lat"`
	SiteLon           *float64             `json:"site_lon"`
	SiteElevM         *float64             `json:"site_elevation_m,omitempty"`
	QualityTier       string               `json:"quality_tier,omitempty"`
	LimitingMagnitude *float64             `json:"limiting_magnitude,omitempty"`
	MountLimits       *wire.MountLimits    `json:"mount_limits,omitempty"`
	HorizonProfile    *wire.HorizonProfile `json:"horizon_profile,omitempty"`
	ObstructionMask   wire.ObstructionMask `json:"obstruction_mask,omitempty"`
}

// LoadConfig reads env vars plus the safety JSON file they point to.
// Fails closed: a missing or unparseable safety file is an error, not a
// "no restrictions" default - matches shared.PassesAltAzSafety's own
// fail-closed rule for missing site coordinates (#405/#453/#454).
func LoadConfig() (*Config, error) {
	cfg := &Config{
		NodeID:             os.Getenv("SAUCEPAN_NODE_ID"),
		AlpacaBaseURL:      envOr("ALPACA_BASE_URL", "http://localhost:11111"),
		TelescopeNum:       envIntOr("ALPACA_TELESCOPE_NUM", 0),
		CameraNum:          envIntOr("ALPACA_CAMERA_NUM", 0),
		FilterWheelNum:     envIntOr("ALPACA_FILTERWHEEL_NUM", 0),
		CaptureDir:         envOr("PIER_CAPTURE_DIR", "./captures"),
		TelemetryPeriod:    time.Duration(envIntOr("PIER_TELEMETRY_PERIOD_S", 30)) * time.Second,
		MQTTBroker:         os.Getenv("MQTT_BROKER"),
		SafetyConfigPath:   os.Getenv("PIER_SAFETY_CONFIG"),
		APIURL:             strings.TrimRight(os.Getenv("SAUCEPAN_API_URL"), "/"),
		DeviceToken:        os.Getenv("SAUCEPAN_DEVICE_TOKEN"),
		UploadChunkSize:    envIntOr("PIER_UPLOAD_CHUNK_BYTES", defaultUploadChunkSize),
		PierCodeRunnerPath: os.Getenv("SAUCEPAN_PIER_CODE_RUNNER"),
		PierCodeCacheDir:   os.Getenv("SAUCEPAN_PIER_CODE_CACHE_DIR"),
	}
	if cfg.PierCodeRunnerPath != "" && cfg.PierCodeCacheDir == "" {
		cfg.PierCodeCacheDir = cfg.CaptureDir + "/piercode-cache"
	}
	if cfg.NodeID == "" {
		return nil, fmt.Errorf("SAUCEPAN_NODE_ID is required")
	}
	if cfg.SafetyConfigPath == "" {
		return nil, fmt.Errorf("PIER_SAFETY_CONFIG is required (path to a JSON file with site_lat/site_lon and, if applicable, mount_limits/horizon_profile/obstruction_mask) - fail-closed, no default site")
	}

	safety, err := loadSafetyConfig(cfg.SafetyConfigPath)
	if err != nil {
		return nil, err
	}
	cfg.Safety = *safety
	return cfg, nil
}

func loadSafetyConfig(path string) (*SafetyConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read safety config %s: %w", path, err)
	}
	var cfg SafetyConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse safety config %s: %w", path, err)
	}
	if cfg.SiteLat == nil || cfg.SiteLon == nil {
		return nil, fmt.Errorf("safety config %s: site_lat and site_lon are both required (fail-closed - see #405/#453/#454)", path)
	}
	if err := shared.ValidateObstructionMask(cfg.ObstructionMask); err != nil {
		return nil, fmt.Errorf("safety config %s: obstruction_mask: %w", path, err)
	}
	return &cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envIntOr(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}
