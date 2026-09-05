package main

import "encoding/json"

// ── Models ─────────────────────────────────────────────────────────────

type Telescope struct {
	TelescopeID        string   `json:"telescope_id"`
	Name               string   `json:"name,omitempty"`
	Power              float64  `json:"power"`
	AvailableFilters   []string `json:"available_filters"`
	ApertureMM         float64  `json:"aperture_mm,omitempty"`
	QE                 float64  `json:"qe,omitempty"`
	FocalLengthMM      float64  `json:"focal_length_mm,omitempty"`
	PixelSizeUM        float64  `json:"pixel_size_um,omitempty"`
	SiteLatitude       *float64 `json:"site_latitude,omitempty"`
	SiteLongitude      *float64 `json:"site_longitude,omitempty"`
	SeeingArcsec       float64  `json:"median_seeing_arcsec,omitempty"`
	LimitingMagnitude  *float64 `json:"limiting_magnitude,omitempty"`
	FOVWidthArcmin     float64  `json:"fov_width_arcmin,omitempty"`
	FOVHeightArcmin    float64  `json:"fov_height_arcmin,omitempty"`
	MountType          int      `json:"mount_type,omitempty"`
	MaxStableExposureS float64  `json:"max_stable_exposure_s,omitempty"`
	IsEmulator         bool     `json:"is_emulator,omitempty"`
	EnabledCampaignIDs []string `json:"enabled_campaign_ids,omitempty"`
}

type Task struct {
	InternalID                   int             `json:"-"`
	PublicID                     string          `json:"id"`
	Name                         string          `json:"name"`
	Description                  string          `json:"description,omitempty"`
	Priority                     int             `json:"priority"`
	Status                       string          `json:"status"`
	IntegrationTime              float64         `json:"integration_time,omitempty"`
	NormalizedIntegrationBudgetS float64         `json:"normalized_integration_budget_s,omitempty"`
	NormalizedIntegrationEarnedS float64         `json:"normalized_integration_earned_s,omitempty"`
	MinPower                     float64         `json:"min_power,omitempty"`
	TargetMagnitude              *float64        `json:"target_magnitude,omitempty"`
	RequiredFilters              []string        `json:"required_filters,omitempty"`
	TargetRA                     float64         `json:"target_ra,omitempty"`
	TargetDec                    float64         `json:"target_dec,omitempty"`
	MinAltitudeDeg               float64         `json:"min_altitude_deg,omitempty"`
	ScienceBand                  string          `json:"science_band,omitempty"`
	MaxPSFFWHMArcsec             float64         `json:"max_psf_fwhm_arcsec,omitempty"`
	MinPSFFWHMArcsec             float64         `json:"min_psf_fwhm_arcsec,omitempty"`
	MinApertureMM                float64         `json:"min_aperture_mm,omitempty"`
	MinSubExposureS              float64         `json:"min_sub_exposure_s,omitempty"`
	MinResolutionArcsec          float64         `json:"min_resolution_arcsec,omitempty"`
	MaxResolutionArcsec          float64         `json:"max_resolution_arcsec,omitempty"`
	FOVWidthArcmin               float64         `json:"required_fov_width_arcmin,omitempty"`
	FOVHeightArcmin              float64         `json:"required_fov_height_arcmin,omitempty"`
	AssignedTelescopeID          *string         `json:"assigned_telescope_id,omitempty"`
	AllowEmulator                bool            `json:"allow_emulator,omitempty"`
	ProductMode                  string          `json:"product_mode,omitempty"`
	CampaignID                   string          `json:"campaign_id,omitempty"`
	DeveloperUserID              *string         `json:"-"`
	OriginalSpec                 json.RawMessage `json:"-"`
}

// Campaign is a researcher-owned folder of tasks.
type Campaign struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	Description      string          `json:"description,omitempty"`
	Status           string          `json:"status"`
	CreatedBy        string          `json:"created_by,omitempty"`
	PointsMultiplier float64         `json:"points_multiplier"`
	TestOnly         bool            `json:"test_only"`
	PackJSON         json.RawMessage `json:"pack_json,omitempty"`
	CompStars        json.RawMessage `json:"comp_stars,omitempty"`
	CreatedAt        string          `json:"created_at,omitempty"`
	ExpandedAt       *string         `json:"expanded_at,omitempty"`
}
