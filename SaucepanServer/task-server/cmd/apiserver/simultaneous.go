package main

import (
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/saucepan/hotpath/shared/simultaneous"
)

func handleGetObservationGroup(w http.ResponseWriter, r *http.Request) {
	gid := r.PathValue("id")
	ctx, cancel := campaignCtx(r.Context())
	defer cancel()

	claims, ok := mustClaims(w, r, "authentication required")
	if !ok {
		return
	}
	if err := assertObservationGroupAccess(ctx, gid, claims.UserID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, 404, "Observation group not found")
			return
		}
		writeError(w, 500, "Database error")
		return
	}

	var (
		status              string
		campaignID          *string
		epoch               time.Time
		deltaT              float64
		siteCount           int
		minBaseline         float64
		projectedBaseline   *float64
		targetRA, targetDec *float64
	)
	err := db.QueryRow(ctx, `
		SELECT status, campaign_id::text, epoch_utc, delta_t_max_s, site_count,
		       min_projected_baseline_km, projected_baseline_km, target_ra, target_dec
		FROM observation_groups WHERE id = $1::uuid
	`, gid).Scan(&status, &campaignID, &epoch, &deltaT, &siteCount, &minBaseline,
		&projectedBaseline, &targetRA, &targetDec)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, 404, "Observation group not found")
			return
		}
		writeError(w, 500, "Database error")
		return
	}

	rows, err := db.Query(ctx, `
		SELECT telescope_id, site_role, status, requested_mid_utc, measured_mid_utc,
		       measured_timing_uncertainty_s, site_latitude, site_longitude
		FROM observation_group_members WHERE group_id = $1::uuid
		ORDER BY site_role
	`, gid)
	if err != nil {
		writeError(w, 500, "Database error")
		return
	}
	defer rows.Close()

	var members []map[string]any
	usable := 0
	for rows.Next() {
		var tel, role, mstatus string
		var reqMid, measMid *time.Time
		var unc, lat, lon *float64
		if err := rows.Scan(&tel, &role, &mstatus, &reqMid, &measMid, &unc, &lat, &lon); err != nil {
			writeError(w, 500, "Database error")
			return
		}
		if mstatus == "delivered" || mstatus == "on_target" {
			usable++
		}
		members = append(members, map[string]any{
			"telescope_id": tel, "site_role": role, "status": mstatus,
			"requested_mid_utc": reqMid, "measured_mid_utc": measMid,
			"measured_timing_uncertainty_s": unc, "site_latitude": lat, "site_longitude": lon,
		})
	}
	if err := rows.Err(); err != nil {
		writeError(w, 500, "Database error")
		return
	}

	writeJSON(w, 200, map[string]any{
		"id": gid, "campaign_id": campaignID, "status": status,
		"scientific_success": simultaneous.IsScientificSuccess(status),
		"epoch_utc":          epoch.UTC().Format(time.RFC3339Nano), "delta_t_max_s": deltaT,
		"site_count": siteCount, "min_projected_baseline_km": minBaseline,
		"projected_baseline_km": projectedBaseline, "target_ra": targetRA, "target_dec": targetDec,
		"usable_members": usable, "members": members,
	})
}
