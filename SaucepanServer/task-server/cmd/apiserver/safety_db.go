package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/saucepan/hotpath/shared"
)

func loadTelescopeSafety(ctx context.Context, telescopeID string) (shared.TelescopeSafety, error) {
	var siteLat, siteLon *float64
	var obstructionRaw, mountLimitsRaw, horizonRaw []byte
	err := db.QueryRow(ctx, `
		SELECT site_latitude, site_longitude,
		       obstruction_mask, mount_limits, horizon_profile
		FROM telescopes WHERE telescope_id = $1
	`, telescopeID).Scan(&siteLat, &siteLon, &obstructionRaw, &mountLimitsRaw, &horizonRaw)
	if err != nil {
		return shared.TelescopeSafety{}, err
	}

	obstructionMask, err := shared.ParseObstructionMask(string(obstructionRaw))
	if err != nil {
		return shared.TelescopeSafety{}, fmt.Errorf("invalid obstruction mask: %w", err)
	}
	safety := shared.TelescopeSafety{
		ObstructionMask: obstructionMask,
		MountLimits:     shared.ParseMountLimits(string(mountLimitsRaw)),
		HorizonProfile:  shared.ParseHorizonProfile(string(horizonRaw)),
		SiteLat:         siteLat,
		SiteLon:         siteLon,
	}

	if snap, ok := getTelemetrySnapshot(telescopeID); ok {
		if snap.MountAltDeg != nil {
			safety.MountAltDeg = snap.MountAltDeg
		}
		if snap.MountAzDeg != nil {
			safety.MountAzDeg = snap.MountAzDeg
		}
		if snap.Location != nil && safety.SiteLat == nil && safety.SiteLon == nil {
			lat, lon := snap.Location.Latitude, snap.Location.Longitude
			safety.SiteLat = &lat
			safety.SiteLon = &lon
		}
		if len(snap.ObstructionMaskLive) > 0 {
			mask, err := shared.ParseObstructionMask(string(snap.ObstructionMaskLive))
			if err != nil {
				return shared.TelescopeSafety{}, fmt.Errorf("invalid live obstruction mask: %w", err)
			}
			safety.ObstructionMask = mask
		}
	}
	return safety, nil
}

func liveObstructionMask(telescopeID string) (shared.ObstructionMask, error) {
	snap, ok := getTelemetrySnapshot(telescopeID)
	if !ok || len(snap.ObstructionMaskLive) == 0 {
		return nil, nil
	}
	return shared.ParseObstructionMask(string(snap.ObstructionMaskLive))
}

func parseObstructionJSON(raw json.RawMessage) (shared.ObstructionMask, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var mask shared.ObstructionMask
	if err := json.Unmarshal(raw, &mask); err != nil {
		return nil, err
	}
	if err := shared.ValidateObstructionMask(mask); err != nil {
		return nil, err
	}
	return mask, nil
}
