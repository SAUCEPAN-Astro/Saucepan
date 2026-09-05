package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/saucepan/hotpath/shared"
)

// ── Handlers ───────────────────────────────────────────────────────────

func handleCreateTask(w http.ResponseWriter, r *http.Request) {
	var task Task
	if err := decodeJSON(r, &task); err != nil {
		writeError(w, 400, "Invalid JSON: "+err.Error())
		return
	}
	if task.Name == "" {
		writeError(w, 400, "name is required")
		return
	}
	ctx, cancel := taskLookupCtx(r.Context())
	defer cancel()
	var developerUserID *string
	if claims := claimsFromContext(ctx); claims != nil && claims.UserID != "" {
		developerUserID = &claims.UserID
	}
	var internalID int
	var publicID string
	err := db.QueryRow(ctx, `
		INSERT INTO tasks (
			name, priority, status, integration_time, normalized_integration_budget_s,
			min_power,
			target_magnitude,
			required_filters, target_ra, target_dec, min_altitude_deg, allow_emulator,
			min_aperture_mm, min_sub_exposure_s, min_resolution_arcsec, max_resolution_arcsec,
			min_psf_fwhm_arcsec, max_psf_fwhm_arcsec,
			required_fov_width_arcmin, required_fov_height_arcmin, science_band, developer_user_id
		) VALUES ($1, $2, 'pending', $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21)
		RETURNING id, public_id::text
	`,
		task.Name, task.Priority, task.IntegrationTime, nullFloat(task.NormalizedIntegrationBudgetS),
		task.MinPower,
		task.TargetMagnitude,
		task.RequiredFilters, task.TargetRA, task.TargetDec, task.MinAltitudeDeg,
		task.AllowEmulator,
		nullFloat(task.MinApertureMM), nullFloat(task.MinSubExposureS),
		nullFloat(task.MinResolutionArcsec), nullFloat(task.MaxResolutionArcsec),
		nullFloat(task.MinPSFFWHMArcsec), nullFloat(task.MaxPSFFWHMArcsec),
		nullFloat(task.FOVWidthArcmin), nullFloat(task.FOVHeightArcmin),
		nullString(task.ScienceBand), developerUserID,
	).Scan(&internalID, &publicID)
	if err != nil {
		log.Printf("DB insert task: %v", err)
		writeError(w, 500, "Database error")
		return
	}
	log.Printf("Created task %s (internal=%d): %s (priority=%d, allow_emulator=%v)", publicID, internalID, task.Name, task.Priority, task.AllowEmulator)
	writeJSON(w, 200, map[string]interface{}{
		"task": map[string]interface{}{"id": publicID},
	})
}

func handleGetTaskByID(w http.ResponseWriter, r *http.Request) {
	ref := r.PathValue("id")
	ctx, cancel := taskLookupCtx(r.Context())
	defer cancel()
	resolved, err := resolveTaskRef(ctx, ref)
	if err != nil {
		writeJSON(w, 200, map[string]interface{}{"task": nil})
		return
	}
	var task Task
	err = db.QueryRow(ctx, `
		SELECT id, public_id::text, name, priority, status, integration_time,
		       normalized_integration_budget_s, normalized_integration_earned_s,
		       target_magnitude, target_ra, target_dec, assigned_telescope_id
		FROM tasks WHERE id = $1
	`, resolved.InternalID).Scan(
		&task.InternalID, &task.PublicID, &task.Name, &task.Priority, &task.Status,
		&task.IntegrationTime, &task.NormalizedIntegrationBudgetS, &task.NormalizedIntegrationEarnedS,
		&task.TargetMagnitude,
		&task.TargetRA, &task.TargetDec,
		&task.AssignedTelescopeID,
	)
	if err != nil {
		writeJSON(w, 200, map[string]interface{}{"task": nil})
		return
	}
	writeJSON(w, 200, map[string]interface{}{"task": task})
}

func handlePatchTask(w http.ResponseWriter, r *http.Request) {
	ref := r.PathValue("id")
	var patch Task
	if err := decodeJSON(r, &patch); err != nil {
		writeError(w, 400, "Invalid JSON: "+err.Error())
		return
	}
	ctx, cancel := taskLookupCtx(r.Context())
	defer cancel()
	resolved, err := resolveTaskRef(ctx, ref)
	if err != nil {
		writeError(w, 404, "Task not found")
		return
	}
	if err := authorizeResearcherTask(ctx, resolved.InternalID); err != nil {
		if errors.Is(err, errUploadTaskNotFound) {
			writeError(w, 404, "Task not found")
		} else {
			writeError(w, 403, "Task access denied")
		}
		return
	}

	var status string
	var assigned *string
	err = db.QueryRow(ctx, `
		SELECT status, assigned_telescope_id FROM tasks WHERE id = $1
	`, resolved.InternalID).Scan(&status, &assigned)
	if err != nil {
		writeError(w, 404, "Task not found")
		return
	}
	if status != "pending" || assigned != nil {
		writeError(w, 409, "Only unassigned pending tasks can be edited")
		return
	}

	// Load full row for supersede copy.
	var existing Task
	var campaignID, developerUserID *string
	var originalSpec json.RawMessage
	err = db.QueryRow(ctx, `
		SELECT id, public_id::text, name, priority, status, integration_time,
		       normalized_integration_budget_s, normalized_integration_earned_s,
		       min_power, target_magnitude, required_filters, target_ra, target_dec, min_altitude_deg, allow_emulator,
		       min_aperture_mm, min_sub_exposure_s, min_resolution_arcsec, max_resolution_arcsec,
		       min_psf_fwhm_arcsec, max_psf_fwhm_arcsec,
		       required_fov_width_arcmin, required_fov_height_arcmin, science_band,
		       campaign_id::text, developer_user_id::text, original_spec, product_mode
		FROM tasks WHERE id = $1
	`, resolved.InternalID).Scan(
		&existing.InternalID, &existing.PublicID, &existing.Name, &existing.Priority, &existing.Status,
		&existing.IntegrationTime, &existing.NormalizedIntegrationBudgetS, &existing.NormalizedIntegrationEarnedS,
		&existing.MinPower, &existing.TargetMagnitude, &existing.RequiredFilters,
		&existing.TargetRA, &existing.TargetDec, &existing.MinAltitudeDeg, &existing.AllowEmulator,
		&existing.MinApertureMM, &existing.MinSubExposureS,
		&existing.MinResolutionArcsec, &existing.MaxResolutionArcsec,
		&existing.MinPSFFWHMArcsec, &existing.MaxPSFFWHMArcsec,
		&existing.FOVWidthArcmin, &existing.FOVHeightArcmin, &existing.ScienceBand,
		&campaignID, &developerUserID, &originalSpec, &existing.ProductMode,
	)
	if err != nil {
		writeError(w, 500, "Database error")
		return
	}
	if campaignID != nil {
		existing.CampaignID = *campaignID
	}
	existing.DeveloperUserID = developerUserID
	existing.OriginalSpec = originalSpec

	applyTaskPatch(&existing, &patch)

	tx, err := db.Begin(ctx)
	if err != nil {
		writeError(w, 500, "Database error")
		return
	}
	defer tx.Rollback(ctx)

	var newInternalID int
	var newPublicID string
	err = tx.QueryRow(ctx, `
		INSERT INTO tasks (
			name, priority, status, integration_time, normalized_integration_budget_s,
			normalized_integration_earned_s, min_power, target_magnitude,
			required_filters, target_ra, target_dec, min_altitude_deg, allow_emulator,
			min_aperture_mm, min_sub_exposure_s, min_resolution_arcsec, max_resolution_arcsec,
			min_psf_fwhm_arcsec, max_psf_fwhm_arcsec,
			required_fov_width_arcmin, required_fov_height_arcmin, science_band,
			campaign_id, developer_user_id, original_spec, product_mode
		) VALUES ($1, $2, 'pending', $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22::uuid, $23::uuid, $24::jsonb, $25)
		RETURNING id, public_id::text
	`,
		existing.Name, existing.Priority, existing.IntegrationTime,
		nullFloat(existing.NormalizedIntegrationBudgetS), existing.NormalizedIntegrationEarnedS,
		existing.MinPower,
		existing.TargetMagnitude,
		existing.RequiredFilters, existing.TargetRA, existing.TargetDec, existing.MinAltitudeDeg,
		existing.AllowEmulator,
		nullFloat(existing.MinApertureMM), nullFloat(existing.MinSubExposureS),
		nullFloat(existing.MinResolutionArcsec), nullFloat(existing.MaxResolutionArcsec),
		nullFloat(existing.MinPSFFWHMArcsec), nullFloat(existing.MaxPSFFWHMArcsec),
		nullFloat(existing.FOVWidthArcmin), nullFloat(existing.FOVHeightArcmin),
		nullString(existing.ScienceBand),
		nullString(existing.CampaignID), nullStringPtr(existing.DeveloperUserID),
		nullJSON(existing.OriginalSpec), defaultProductMode(existing.ProductMode),
	).Scan(&newInternalID, &newPublicID)
	if err != nil {
		log.Printf("DB supersede insert: %v", err)
		writeError(w, 500, "Database error")
		return
	}

	_, err = tx.Exec(ctx, `
		UPDATE tasks SET status = 'superseded', updated_at = NOW() WHERE id = $1
	`, resolved.InternalID)
	if err != nil {
		writeError(w, 500, "Database error")
		return
	}

	patchJSON, _ := json.Marshal(patch)
	var actorID *string
	if claims := claimsFromContext(r.Context()); claims != nil && claims.UserID != "" {
		actorID = &claims.UserID
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO task_revisions (old_public_id, new_public_id, old_task_id, new_task_id, actor_user_id, patch)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, existing.PublicID, newPublicID, resolved.InternalID, newInternalID, actorID, patchJSON)
	if err != nil {
		writeError(w, 500, "Database error")
		return
	}

	if err := tx.Commit(ctx); err != nil {
		writeError(w, 500, "Database error")
		return
	}

	writeJSON(w, 200, map[string]interface{}{
		"task": map[string]interface{}{
			"id":              newPublicID,
			"supersedes_id":   existing.PublicID,
			"internal_id_old": resolved.InternalID,
			"internal_id_new": newInternalID,
		},
	})
}

func defaultProductMode(mode string) string {
	if mode == "" {
		return "per_frame"
	}
	return mode
}

func applyTaskPatch(dst, patch *Task) {
	if patch.Name != "" {
		dst.Name = patch.Name
	}
	if patch.Priority != 0 {
		dst.Priority = patch.Priority
	}
	if patch.IntegrationTime != 0 {
		dst.IntegrationTime = patch.IntegrationTime
	}
	if patch.NormalizedIntegrationBudgetS != 0 {
		dst.NormalizedIntegrationBudgetS = patch.NormalizedIntegrationBudgetS
	}
	if patch.MinPower != 0 {
		dst.MinPower = patch.MinPower
	}
	if patch.TargetMagnitude != nil {
		dst.TargetMagnitude = patch.TargetMagnitude
	}
	if len(patch.RequiredFilters) > 0 {
		dst.RequiredFilters = patch.RequiredFilters
	}
	if patch.TargetRA != 0 {
		dst.TargetRA = patch.TargetRA
	}
	if patch.TargetDec != 0 {
		dst.TargetDec = patch.TargetDec
	}
	if patch.MinAltitudeDeg != 0 {
		dst.MinAltitudeDeg = patch.MinAltitudeDeg
	}
	if patch.ScienceBand != "" {
		dst.ScienceBand = patch.ScienceBand
	}
	if patch.MaxPSFFWHMArcsec != 0 {
		dst.MaxPSFFWHMArcsec = patch.MaxPSFFWHMArcsec
	}
	if patch.MinPSFFWHMArcsec != 0 {
		dst.MinPSFFWHMArcsec = patch.MinPSFFWHMArcsec
	}
	if patch.MinApertureMM != 0 {
		dst.MinApertureMM = patch.MinApertureMM
	}
	if patch.MinSubExposureS != 0 {
		dst.MinSubExposureS = patch.MinSubExposureS
	}
	if patch.MinResolutionArcsec != 0 {
		dst.MinResolutionArcsec = patch.MinResolutionArcsec
	}
	if patch.MaxResolutionArcsec != 0 {
		dst.MaxResolutionArcsec = patch.MaxResolutionArcsec
	}
	if patch.FOVWidthArcmin != 0 {
		dst.FOVWidthArcmin = patch.FOVWidthArcmin
	}
	if patch.FOVHeightArcmin != 0 {
		dst.FOVHeightArcmin = patch.FOVHeightArcmin
	}
	dst.AllowEmulator = patch.AllowEmulator || dst.AllowEmulator
}

func handleCompleteTask(w http.ResponseWriter, r *http.Request) {
	ref := r.PathValue("id")
	ctx, cancel := taskLookupCtx(r.Context())
	defer cancel()
	resolved, err := resolveTaskRef(ctx, ref)
	if err != nil {
		writeError(w, 400, "Invalid task ID")
		return
	}
	if err := authorizeResearcherTask(ctx, resolved.InternalID); err != nil {
		if errors.Is(err, errUploadTaskNotFound) {
			writeError(w, 404, "Task not found")
		} else {
			writeError(w, 403, "Task access denied")
		}
		return
	}
	tag, err := db.Exec(ctx, `
		UPDATE tasks SET status = $1, updated_at = NOW()
		WHERE id = $2 AND status IN ($3, $4, $5)
	`, shared.TaskStatusCompleted, resolved.InternalID,
		shared.TaskStatusPending, shared.TaskStatusAssigned, shared.TaskStatusInProgress)
	if err != nil {
		log.Printf("DB complete task %s: %v", ref, err)
		writeError(w, 500, "Database error")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, 409, "Task is not open for completion")
		return
	}
	// Flip every active cohort assignment to completed (#402). Best-effort: the
	// task row is the source of truth for lifecycle; the join table is for auth.
	if _, err := db.Exec(ctx, `
		UPDATE task_assignments SET status = 'completed', updated_at = NOW()
		WHERE task_id = $1 AND status IN ('assigned', 'in_progress')
	`, resolved.InternalID); err != nil {
		log.Printf("DB complete task %s: task_assignments update: %v", ref, err)
	}
	// #404: completion is a defined clear point for the orchestrator-owned
	// current_task_* fields in the Redis hot-path cache — free the node(s) so
	// the scheduler stops treating them as busy on a task that is now done.
	clearNodeAssignmentForTask(ctx, resolved.InternalID)
	log.Printf("Task %s marked completed", resolved.PublicID)
	writeJSON(w, 200, map[string]interface{}{"success": true, "message": "Task completed"})
}
