package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/saucepan/hotpath/shared/grading"
)

func handleGradesIngest(w http.ResponseWriter, r *http.Request) {
	if !gradesIngestTokenValid(r) {
		writeError(w, 401, "Unauthorized")
		return
	}

	var data map[string]any
	if err := decodeJSON(r, &data); err != nil {
		writeError(w, 400, "JSON body required")
		return
	}

	body, status, err := ingestGradePayload(r.Context(), data)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, status, body)
}

func handleTelescopePoints(w http.ResponseWriter, r *http.Request) {
	externalID := r.PathValue("id")
	if externalID == "" {
		writeError(w, 400, "telescope id required")
		return
	}

	ctx := r.Context()
	var repStats []byte
	err := db.QueryRow(ctx,
		`SELECT COALESCE(reputation_stats, '{}') FROM telescopes WHERE telescope_id = $1`,
		externalID,
	).Scan(&repStats)
	if err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, 404, fmt.Sprintf("Telescope %q not found", externalID))
			return
		}
		writeError(w, 500, "database error: "+err.Error())
		return
	}

	stats := map[string]any{}
	_ = json.Unmarshal(repStats, &stats)

	rows, err := db.Query(ctx,
		`SELECT id, upload_id, idempotency_key, task_id, headline_grade, points_earned,
		        stack_eligible, sp_exptime, grader_version, created_at
		 FROM frame_grades WHERE telescope_id = $1 ORDER BY created_at DESC LIMIT 20`,
		externalID,
	)
	if err != nil {
		writeError(w, 500, "database error: "+err.Error())
		return
	}
	defer rows.Close()

	recent := []map[string]any{}
	for rows.Next() {
		var (
			id, headline                 int
			uploadID, idemKey, graderVer *string
			taskID                       *int
			points, oaExptime            float64
			stackEligible                bool
			createdAt                    any
		)
		if err := rows.Scan(&id, &uploadID, &idemKey, &taskID, &headline, &points,
			&stackEligible, &oaExptime, &graderVer, &createdAt); err != nil {
			writeError(w, 500, "scan error: "+err.Error())
			return
		}
		frame := map[string]any{
			"id":             id,
			"headline_grade": headline,
			"points_earned":  points,
			"stack_eligible": stackEligible,
			"sp_exptime":     oaExptime,
			"created_at":     createdAt,
		}
		if uploadID != nil {
			frame["upload_id"] = *uploadID
		}
		if idemKey != nil {
			frame["idempotency_key"] = *idemKey
		}
		if taskID != nil {
			frame["task_id"] = *taskID
		}
		if graderVer != nil {
			frame["grader_version"] = *graderVer
		}
		recent = append(recent, frame)
	}

	writeJSON(w, 200, map[string]any{
		"telescope_id":           externalID,
		"total_points":           stats["total_points"],
		"frame_count":            stats["frame_count"],
		"total_exposure_seconds": stats["total_exposure_seconds"],
		"points_per_hour":        stats["points_per_hour"],
		"reliability_score":      stats["reliability_score"],
		"task_quality_score":     stats["task_quality_score"],
		"recent_frames":          recent,
	})
}

func ingestGradePayload(ctx context.Context, data map[string]any) (map[string]any, int, error) {
	idempotencyKey, _ := data["idempotency_key"].(string)
	if idempotencyKey == "" {
		return map[string]any{"error": "Missing idempotency_key"}, 400, nil
	}

	var existingID int
	err := db.QueryRow(ctx,
		`SELECT id FROM frame_grades WHERE idempotency_key = $1`, idempotencyKey,
	).Scan(&existingID)
	if err == nil {
		return map[string]any{
			"message":     "Duplicate idempotency_key",
			"frame_grade": map[string]any{"id": existingID, "idempotency_key": idempotencyKey},
		}, 409, nil
	}
	if err != pgx.ErrNoRows {
		return nil, 0, fmt.Errorf("duplicate check: %w", err)
	}

	telescopeExternal, _ := data["telescope_id"].(string)
	if telescopeExternal == "" {
		return map[string]any{"error": "Missing telescope_id"}, 400, nil
	}

	var repStatsRaw []byte
	err = db.QueryRow(ctx,
		`SELECT COALESCE(reputation_stats, '{}') FROM telescopes WHERE telescope_id = $1`,
		telescopeExternal,
	).Scan(&repStatsRaw)
	if err != nil {
		if err == pgx.ErrNoRows {
			return map[string]any{"error": fmt.Sprintf("Telescope %q not found", telescopeExternal)}, 404, nil
		}
		return nil, 0, fmt.Errorf("telescope lookup: %w", err)
	}

	dimensions, _ := data["dimensions"].(map[string]any)
	if dimensions == nil {
		dimensions = map[string]any{}
	}

	headline := intFromAny(data["headline"])
	oaExptime := resolveOAExptime(data, dimensions)

	stats := map[string]any{}
	_ = json.Unmarshal(repStatsRaw, &stats)

	var taskPK *int
	campaignMultiplier := 1.0
	if taskID := data["task_id"]; taskID != nil {
		if tid, ok := intFromAnyOK(taskID); ok {
			var exists int
			if err := db.QueryRow(ctx, `SELECT id FROM tasks WHERE id = $1`, tid).Scan(&exists); err == nil {
				taskPK = &tid
				_ = db.QueryRow(ctx, `
					SELECT COALESCE(c.points_multiplier, 1.0)
					FROM tasks t
					LEFT JOIN campaigns c ON t.campaign_id = c.id
					WHERE t.id = $1
				`, tid).Scan(&campaignMultiplier)
			}
		}
	}

	// #257: reject foreign object keys before any DB write / inbox presign path.
	if err := validateGradeIngestObjectKeys(ctx, data, taskPK, telescopeExternal); err != nil {
		return map[string]any{"error": err.Error()}, 400, nil
	}

	breakdown := grading.ComputeFramePoints(map[string]any{
		"dimensions": dimensions,
		"sp_exptime": oaExptime,
	}, stats, campaignMultiplier)
	stackEligible := grading.IsStackEligible(dimensions)

	breakdownJSON, _ := json.Marshal(breakdown)
	dimensionsJSON, _ := json.Marshal(dimensions)
	qualityMetrics, _ := data["quality_metrics"].(map[string]any)
	if qualityMetrics == nil {
		qualityMetrics = map[string]any{}
	}
	if v, ok := data["sp_emulator"]; ok {
		qualityMetrics["sp_emulator"] = v
	}
	if v, ok := data["data_tier"].(string); ok && v != "" {
		qualityMetrics["data_tier"] = v
	}
	if v, ok := data["science_eligible"]; ok {
		qualityMetrics["science_eligible"] = v
	}
	if v, ok := data["object_key"].(string); ok && v != "" {
		qualityMetrics["object_key"] = v
	}
	qualityMetricsJSON, _ := json.Marshal(qualityMetrics)

	if taskPK != nil {
		_, _ = db.Exec(ctx,
			`UPDATE tasks SET accumulated_exposure_seconds = COALESCE(accumulated_exposure_seconds, 0) + $1 WHERE id = $2`,
			max(0.0, oaExptime), *taskPK,
		)
	}

	uploadID, _ := data["upload_id"].(string)
	graderVersion, _ := data["grader_version"].(string)

	var frameID int
	err = db.QueryRow(ctx,
		`INSERT INTO frame_grades (
			upload_id, idempotency_key, task_id, telescope_id, telescope_external_id,
			headline_grade, dimensions, points_earned, points_breakdown, stack_eligible,
			sp_exptime, grader_version, quality_metrics, created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,NOW())
		RETURNING id`,
		nullString(uploadID), idempotencyKey, taskPK, telescopeExternal, telescopeExternal,
		headline, dimensionsJSON, breakdown.PointsEarned, breakdownJSON, stackEligible,
		oaExptime, nullString(graderVersion), qualityMetricsJSON,
	).Scan(&frameID)
	if err != nil {
		return nil, 0, fmt.Errorf("insert frame_grade: %w", err)
	}

	if err := applyTaskBudgetDebit(ctx, taskPK, telescopeExternal, oaExptime, stackEligible); err != nil {
		return nil, 0, err
	}

	createInboxDeliveryFromGrade(ctx, frameID, taskPK, uploadID, data, breakdown.PointsEarned, stackEligible)
	createDeveloperDeliveryFromGrade(ctx, frameID, taskPK, uploadID, data, stackEligible)

	if taskPK != nil {
		var campaignID *string
		_ = db.QueryRow(ctx, `SELECT campaign_id::text FROM tasks WHERE id = $1`, *taskPK).Scan(&campaignID)
		if campaignID != nil && *campaignID != "" {
			msg := "Frame graded and ingested"
			emitCampaignUpdate(ctx, *campaignID, "frame.graded", msg, taskPK, map[string]any{
				"points_earned":  breakdown.PointsEarned,
				"stack_eligible": stackEligible,
				"telescope_id":   telescopeExternal,
			})
			if !stackEligible {
				emitCampaignAlert(ctx, *campaignID, "frame.rejected", "Frame not stack-eligible — review quality", taskPK, map[string]any{
					"headline_grade": headline,
					"telescope_id":   telescopeExternal,
				})
			} else {
				var eligible int
				_ = db.QueryRow(ctx, `
					SELECT COUNT(*)::int FROM frame_grades
					WHERE task_id = $1 AND stack_eligible = true
				`, *taskPK).Scan(&eligible)
				emitCampaignUpdate(ctx, *campaignID, "stack.progress", "Stack-eligible frame count updated", taskPK, map[string]any{
					"eligible_frames": eligible,
					"telescope_id":    telescopeExternal,
				})
			}
		}
	}

	repPartial := grading.BuildReputationPartial(stats, headline, dimensions, breakdown.PointsEarned, oaExptime)
	merged := grading.MergeReputationStats(stats, repPartial)
	mergedJSON, _ := json.Marshal(merged)
	_, err = db.Exec(ctx, `UPDATE telescopes SET reputation_stats = $1 WHERE telescope_id = $2`, mergedJSON, telescopeExternal)
	if err != nil {
		return nil, 0, fmt.Errorf("update reputation: %w", err)
	}

	catalogID, _ := upsertFrameCatalogFromGrade(ctx, data, telescopeExternal, headline, stackEligible, oaExptime)

	out := map[string]any{
		"message": "Grade ingested",
		"frame_grade": map[string]any{
			"id":               frameID,
			"idempotency_key":  idempotencyKey,
			"telescope_id":     telescopeExternal,
			"points_earned":    breakdown.PointsEarned,
			"stack_eligible":   stackEligible,
			"sp_emulator":      truthyAny(data["sp_emulator"]),
			"data_tier":        data["data_tier"],
			"science_eligible": data["science_eligible"],
		},
		"reputation_stats": merged,
	}
	if catalogID != "" {
		out["frame_catalog_id"] = catalogID
	}
	return out, 201, nil
}
