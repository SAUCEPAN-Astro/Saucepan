package main

import (
	"encoding/json"
	"net/http"
	"strings"
)

const emulatorIDPrefix = "emu_"

func telescopeIsEmulatorID(id string) bool {
	return strings.HasPrefix(id, emulatorIDPrefix)
}

func normalizeEmulatorFlags(t *TelescopeRegistration) error {
	if telescopeIsEmulatorID(t.TelescopeID) {
		t.IsEmulator = true
	}
	if t.IsEmulator && !telescopeIsEmulatorID(t.TelescopeID) {
		return errBadRequest("emulator telescopes must use telescope_id prefix " + emulatorIDPrefix)
	}
	return nil
}

// TelescopeRegistration includes safety fields synced from the client.
type TelescopeRegistration struct {
	Telescope
	ObstructionMask json.RawMessage `json:"obstruction_mask,omitempty"`
	MountLimits     json.RawMessage `json:"mount_limits,omitempty"`
	HorizonProfile  json.RawMessage `json:"horizon_profile,omitempty"`
}

// resolveTelescopeActor returns the user id allowed to claim/update this telescope.
// JWT owners stamp owner_user_id; device tokens may only touch their bound telescope_id.
func resolveTelescopeActor(r *http.Request, telescopeID string) (userID string, err error) {
	if claims := claimsFromContext(r.Context()); claims != nil && claims.UserID != "" {
		return claims.UserID, nil
	}
	if device := uploadDeviceFromContext(r.Context()); device != nil {
		if device.TelescopeID != "" && device.TelescopeID != telescopeID {
			return "", errForbidden("telescope_id does not match authenticated device")
		}
		if device.UserID == "" {
			return "", errForbidden("device has no linked user")
		}
		return device.UserID, nil
	}
	return "", errForbidden("unauthorized")
}

// handleRegisterTelescope handles telescope registration and update.
// Auth: requireDeviceOrJWT. Pier onboarding sends the user JWT after /auth/devices;
// updates also accept the pier device_token when bound to the same telescope_id.
func handleRegisterTelescope(w http.ResponseWriter, r *http.Request) {
	var t TelescopeRegistration
	if err := decodeJSON(r, &t); err != nil {
		writeError(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if t.TelescopeID == "" {
		writeError(w, 400, "telescope_id is required")
		return
	}
	if err := normalizeEmulatorFlags(&t); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if t.Power < 0 || t.Power > 1 {
		writeError(w, 400, "power must be between 0 and 1")
		return
	}
	if len(t.ObstructionMask) > 0 {
		if _, err := parseObstructionJSON(t.ObstructionMask); err != nil {
			writeError(w, 400, err.Error())
			return
		}
	}

	actingUserID, err := resolveTelescopeActor(r, t.TelescopeID)
	if err != nil {
		if _, ok := err.(forbiddenError); ok {
			writeError(w, 403, err.Error())
			return
		}
		writeError(w, 401, "Unauthorized")
		return
	}
	ownerUserID := &actingUserID
	if err := assertCanClaimTelescope(r.Context(), t.TelescopeID, actingUserID); err != nil {
		if _, ok := err.(forbiddenError); ok {
			writeError(w, 403, err.Error())
			return
		}
		writeError(w, 500, "database error: "+err.Error())
		return
	}

	switch r.Method {
	case "POST":
		enabled := t.EnabledCampaignIDs
		if enabled == nil {
			enabled = []string{}
		}
		_, err := db.Exec(r.Context(),
			`INSERT INTO telescopes (telescope_id, name, power, available_filters,
				aperture_mm, qe, focal_length_mm, pixel_size_um, site_latitude, site_longitude,
				median_seeing_arcsec, limiting_magnitude, fov_width_arcmin, fov_height_arcmin, mount_type,
				max_stable_exposure_s,
				obstruction_mask, mount_limits, horizon_profile, is_emulator, enabled_campaign_ids,
				owner_user_id)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22)
			 ON CONFLICT (telescope_id) DO UPDATE SET
				name=EXCLUDED.name, power=EXCLUDED.power, available_filters=EXCLUDED.available_filters,
				aperture_mm=EXCLUDED.aperture_mm, qe=COALESCE(EXCLUDED.qe, telescopes.qe),
				focal_length_mm=EXCLUDED.focal_length_mm,
				pixel_size_um=EXCLUDED.pixel_size_um,
				site_latitude=COALESCE(EXCLUDED.site_latitude, telescopes.site_latitude),
				site_longitude=COALESCE(EXCLUDED.site_longitude, telescopes.site_longitude),
				median_seeing_arcsec=EXCLUDED.median_seeing_arcsec,
				limiting_magnitude=EXCLUDED.limiting_magnitude,
				fov_width_arcmin=EXCLUDED.fov_width_arcmin,
				fov_height_arcmin=EXCLUDED.fov_height_arcmin,
				mount_type=EXCLUDED.mount_type,
				max_stable_exposure_s=COALESCE(EXCLUDED.max_stable_exposure_s, telescopes.max_stable_exposure_s),
				obstruction_mask=COALESCE(EXCLUDED.obstruction_mask, telescopes.obstruction_mask),
				mount_limits=COALESCE(EXCLUDED.mount_limits, telescopes.mount_limits),
				horizon_profile=COALESCE(EXCLUDED.horizon_profile, telescopes.horizon_profile),
				is_emulator=EXCLUDED.is_emulator,
				enabled_campaign_ids=COALESCE(EXCLUDED.enabled_campaign_ids, telescopes.enabled_campaign_ids),
				owner_user_id=COALESCE(telescopes.owner_user_id, EXCLUDED.owner_user_id),
				updated_at=NOW()`,
			t.TelescopeID, t.Name, t.Power, t.AvailableFilters,
			t.ApertureMM, nullFloat(t.QE), t.FocalLengthMM, t.PixelSizeUM,
			t.SiteLatitude, t.SiteLongitude, t.SeeingArcsec, t.LimitingMagnitude,
			t.FOVWidthArcmin, t.FOVHeightArcmin, t.MountType,
			nullFloat(t.MaxStableExposureS),
			nullJSON(t.ObstructionMask), nullJSON(t.MountLimits), nullJSON(t.HorizonProfile),
			t.IsEmulator, enabled, ownerUserID,
		)
		if err != nil {
			writeError(w, 500, "database error: "+err.Error())
			return
		}
		syncTelescopeRedisMeta(&t)
		writeJSON(w, 201, map[string]interface{}{
			"status":       "registered",
			"telescope_id": t.TelescopeID,
			"is_emulator":  t.IsEmulator,
		})

	case "PATCH":
		// Existing owned rows: assertCanClaimTelescope already enforced above.
		_, err := db.Exec(r.Context(),
			`UPDATE telescopes SET
				name=CASE WHEN $2 <> '' THEN $2 ELSE name END,
				power=CASE WHEN $3 > 0 THEN $3 ELSE power END,
				qe=COALESCE($4, qe),
				obstruction_mask=COALESCE($5, obstruction_mask),
				mount_limits=COALESCE($6, mount_limits),
				horizon_profile=COALESCE($7, horizon_profile),
				enabled_campaign_ids=CASE WHEN $8::text[] IS NOT NULL THEN $8::text[] ELSE enabled_campaign_ids END,
				owner_user_id=COALESCE(owner_user_id, $9),
				limiting_magnitude=COALESCE($10, limiting_magnitude),
				updated_at=NOW()
			 WHERE telescope_id=$1`,
			t.TelescopeID, t.Name, t.Power, nullFloat(t.QE),
			nullJSON(t.ObstructionMask), nullJSON(t.MountLimits), nullJSON(t.HorizonProfile),
			nullStringSlice(t.EnabledCampaignIDs), ownerUserID, t.LimitingMagnitude,
		)
		if err != nil {
			writeError(w, 500, "database error: "+err.Error())
			return
		}
		t.IsEmulator = telescopeIsEmulatorID(t.TelescopeID)
		syncTelescopeRedisMeta(&t)
		writeJSON(w, 200, map[string]string{"status": "updated", "telescope_id": t.TelescopeID})

	default:
		writeError(w, 405, "method not allowed")
	}
}

type badRequestError string

func (e badRequestError) Error() string { return string(e) }

func errBadRequest(msg string) error { return badRequestError(msg) }
