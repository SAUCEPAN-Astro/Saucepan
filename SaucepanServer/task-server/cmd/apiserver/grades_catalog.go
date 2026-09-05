package main

import (
	"context"
	"fmt"
)

// upsertFrameCatalogFromGrade mirrors L1 sky/time fields into frame_catalog (#33).
func upsertFrameCatalogFromGrade(
	ctx context.Context,
	data map[string]any,
	telescopeExternal string,
	headline int,
	stackEligible bool,
	oaExptime float64,
) (string, error) {
	uploadID, _ := data["upload_id"].(string)
	objectKey, _ := data["object_key"].(string)
	if objectKey == "" {
		if qm, ok := data["quality_metrics"].(map[string]any); ok {
			objectKey, _ = qm["object_key"].(string)
		}
	}
	if objectKey == "" && uploadID != "" {
		objectKey = "upload/" + uploadID
	}
	if objectKey == "" {
		return "", nil
	}

	ra := floatPtrFromAny(data["sp_ra"])
	dec := floatPtrFromAny(data["sp_dec"])
	dateObs := strFromAny(data["sp_dateobs"])
	filter := strFromAny(data["sp_filter"])
	calstat := strFromAny(data["sp_calstat"])
	fwhm := floatPtrFromAny(data["sp_fwhm"])
	snr := floatPtrFromAny(data["sp_snr"])
	mjd := floatPtrFromAny(data["mjd_obs"])
	airmass := floatPtrFromAny(data["airmass"])
	zp := floatPtrFromAny(data["zp"])
	campaignID := strFromAny(data["campaign_id"])
	frameIDStr := strFromAny(data["frame_id"])
	checksum := strFromAny(data["checksum_sha256"])
	photFlag := strFromAny(data["phot_flag"])
	taskID := ""
	if v := data["task_id"]; v != nil {
		taskID = fmt.Sprint(v)
	}
	if nested, ok := data["frame_catalog"].(map[string]any); ok {
		if ra == nil {
			ra = floatPtrFromAny(nested["ra_deg"])
		}
		if dec == nil {
			dec = floatPtrFromAny(nested["dec_deg"])
		}
		if v, ok := nested["object_key"].(string); ok && v != "" {
			objectKey = v
		}
		if dateObs == "" {
			dateObs = strFromAny(nested["date_obs"])
		}
		if filter == "" {
			filter = strFromAny(nested["filter"])
		}
		if calstat == "" {
			calstat = strFromAny(nested["calstat"])
		}
		if fwhm == nil {
			fwhm = floatPtrFromAny(nested["fwhm_arcsec"])
		}
		if snr == nil {
			snr = floatPtrFromAny(nested["snr"])
		}
		if mjd == nil {
			mjd = floatPtrFromAny(nested["mjd_obs"])
		}
		if airmass == nil {
			airmass = floatPtrFromAny(nested["airmass"])
		}
		if zp == nil {
			zp = floatPtrFromAny(nested["zp"])
		}
		if campaignID == "" {
			campaignID = strFromAny(nested["campaign_id"])
		}
		if frameIDStr == "" {
			frameIDStr = strFromAny(nested["frame_id"])
		}
		if checksum == "" {
			checksum = strFromAny(nested["checksum_sha256"])
		}
		if photFlag == "" {
			photFlag = strFromAny(nested["phot_flag"])
		}
		if taskID == "" {
			taskID = strFromAny(nested["task_id"])
		}
	}

	exptime := oaExptime
	if f := floatPtrFromAny(data["sp_exptime"]); f != nil {
		exptime = *f
	}

	var catalogID string
	err := db.QueryRow(ctx, `
		INSERT INTO frame_catalog (
			id, frame_id, upload_id, telescope_id, task_id, campaign_id, object_key,
			checksum_sha256, date_obs, mjd_obs, ra_deg, dec_deg, filter, exptime_sec,
			airmass, fwhm_arcsec, snr, calstat, phot_flag, headline_grade, stack_eligible, zp, created_at
		) VALUES (
			gen_random_uuid(),
			NULLIF($1, ''), NULLIF($2, ''), $3, NULLIF($4, ''), NULLIF($5, ''), $6,
			NULLIF($7, ''),
			CASE WHEN $8 = '' THEN NULL ELSE $8::timestamptz END,
			$9, $10, $11, NULLIF($12, ''), $13,
			$14, $15, $16, NULLIF($17, ''), NULLIF($18, ''), $19, $20, $21, NOW()
		)
		ON CONFLICT (upload_id) DO UPDATE SET
			frame_id = COALESCE(EXCLUDED.frame_id, frame_catalog.frame_id),
			telescope_id = EXCLUDED.telescope_id,
			task_id = COALESCE(EXCLUDED.task_id, frame_catalog.task_id),
			campaign_id = COALESCE(EXCLUDED.campaign_id, frame_catalog.campaign_id),
			object_key = EXCLUDED.object_key,
			checksum_sha256 = COALESCE(EXCLUDED.checksum_sha256, frame_catalog.checksum_sha256),
			date_obs = COALESCE(EXCLUDED.date_obs, frame_catalog.date_obs),
			mjd_obs = COALESCE(EXCLUDED.mjd_obs, frame_catalog.mjd_obs),
			ra_deg = COALESCE(EXCLUDED.ra_deg, frame_catalog.ra_deg),
			dec_deg = COALESCE(EXCLUDED.dec_deg, frame_catalog.dec_deg),
			filter = COALESCE(EXCLUDED.filter, frame_catalog.filter),
			exptime_sec = COALESCE(EXCLUDED.exptime_sec, frame_catalog.exptime_sec),
			airmass = COALESCE(EXCLUDED.airmass, frame_catalog.airmass),
			fwhm_arcsec = COALESCE(EXCLUDED.fwhm_arcsec, frame_catalog.fwhm_arcsec),
			snr = COALESCE(EXCLUDED.snr, frame_catalog.snr),
			calstat = COALESCE(EXCLUDED.calstat, frame_catalog.calstat),
			phot_flag = COALESCE(EXCLUDED.phot_flag, frame_catalog.phot_flag),
			headline_grade = EXCLUDED.headline_grade,
			stack_eligible = EXCLUDED.stack_eligible,
			zp = COALESCE(EXCLUDED.zp, frame_catalog.zp)
		RETURNING id::text`,
		frameIDStr, uploadID, telescopeExternal, taskID, campaignID, objectKey,
		checksum, dateObs, mjd, ra, dec, filter, exptime,
		airmass, fwhm, snr, calstat, photFlag, headline, stackEligible, zp,
	).Scan(&catalogID)
	if err != nil {
		return "", err
	}
	return catalogID, nil
}
