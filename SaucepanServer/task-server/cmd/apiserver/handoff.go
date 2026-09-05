package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/saucepan/hotpath/shared"
)

// GET /quest/handoff-broadcast — global idle poll hint (Flask parity).
func handleHandoffBroadcast(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	now := time.Now().UTC()

	rows, err := db.Query(ctx, `
		SELECT id, status, target_ra, target_dec, min_altitude_deg,
		       scheduled_end_at, user_end_at, handoff_lead_seconds,
		       emergency_handoff_requested_at
		FROM tasks
		WHERE status IN ('pending', 'assigned', 'in_progress')
	`)
	if err != nil {
		log.Printf("handoff-broadcast: %v", err)
		writeError(w, 500, "database error")
		return
	}
	defer rows.Close()

	var tasks []sharedHandoffTask
	for rows.Next() {
		var t sharedHandoffTask
		err := rows.Scan(
			&t.ID, &t.Status, &t.TargetRA, &t.TargetDec, &t.MinAltitudeDeg,
			&t.ScheduledEndAt, &t.UserEndAt, &t.HandoffLeadSeconds, &t.EmergencyHandoffRequestedAt,
		)
		if err != nil {
			continue
		}
		tasks = append(tasks, t)
	}

	active := anyHandoffBroadcastActive(tasks, now)
	var rec *int
	if active {
		v := broadcastRecommendedPollSeconds(tasks, now)
		rec = &v
	}
	writeJSON(w, 200, map[string]interface{}{
		"any_handoff_active":            active,
		"recommended_idle_poll_seconds": rec,
	})
}

// GET /quest/handoff-status — per-telescope obstruction ETA + urgency.
func handleHandoffStatus(w http.ResponseWriter, r *http.Request) {
	telescopeID := r.URL.Query().Get("telescope_id")
	if telescopeID == "" {
		writeError(w, 400, "Missing telescope_id")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	now := time.Now().UTC()

	safety, err := loadTelescopeSafety(ctx, telescopeID)
	if err != nil {
		writeError(w, 400, "Telescope not found")
		return
	}

	taskIDStr := r.URL.Query().Get("task_id")
	var taskID int64
	if taskIDStr != "" {
		taskID, _ = strconv.ParseInt(taskIDStr, 10, 64)
	}
	if taskID == 0 {
		if snap, ok := getTelemetrySnapshot(telescopeID); ok && snap.TaskID != nil {
			taskID = *snap.TaskID
		}
	}
	if taskID == 0 {
		writeJSON(w, 200, map[string]interface{}{
			"task_id":                           nil,
			"message":                           "No task_id on telemetry; pass task_id query param",
			"urgency":                           "none",
			"recommended_poll_interval_seconds": 3600,
		})
		return
	}

	var ht sharedHandoffTask
	err = db.QueryRow(ctx, `
		SELECT id, status, target_ra, target_dec, min_altitude_deg,
		       scheduled_end_at, user_end_at, handoff_lead_seconds,
		       emergency_handoff_requested_at
		FROM tasks WHERE id = $1
	`, taskID).Scan(
		&ht.ID, &ht.Status, &ht.TargetRA, &ht.TargetDec, &ht.MinAltitudeDeg,
		&ht.ScheduledEndAt, &ht.UserEndAt, &ht.HandoffLeadSeconds, &ht.EmergencyHandoffRequestedAt,
	)
	if err != nil {
		writeError(w, 404, "Task not found")
		return
	}
	if err := validateUploadAssignment(ctx, telescopeID, taskID); err != nil {
		if errors.Is(err, errUploadTaskNotFound) {
			writeError(w, 404, "Task not found")
		} else {
			writeError(w, 403, "Task is not assigned to this telescope")
		}
		return
	}

	maskLive, err := liveObstructionMask(telescopeID)
	if err != nil {
		writeError(w, 400, "Invalid live obstruction mask")
		return
	}
	payload := buildHandoffStatusPayload(ht, telescopeID, safety, now, maskLive)
	writeJSON(w, 200, payload)
}

// POST /quest/tasks/{id}/emergency-handoff
func handleEmergencyHandoff(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	taskID, err := strconv.Atoi(idStr)
	if err != nil {
		writeError(w, 400, "Invalid task ID")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := authorizeEmergencyHandoff(ctx, taskID); err != nil {
		if errors.Is(err, errUploadTaskNotFound) {
			writeError(w, 404, "Task not found")
			return
		}
		writeError(w, 403, "Task access denied")
		return
	}
	result, err := db.Exec(ctx, `
		UPDATE tasks SET emergency_handoff_requested_at = NOW(), updated_at = NOW()
		WHERE id = $1
	`, taskID)
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	if result.RowsAffected() == 0 {
		writeError(w, 404, "Task not found")
		return
	}
	log.Printf("Emergency handoff requested for task %d", taskID)
	writeJSON(w, 200, map[string]string{"message": "Emergency handoff recorded"})
}

func authorizeEmergencyHandoff(ctx context.Context, taskID int) error {
	if device := uploadDeviceFromContext(ctx); device != nil {
		if device.TelescopeID == "" {
			return errForbidden("device is not bound to a telescope")
		}
		return validateUploadAssignment(ctx, device.TelescopeID, int64(taskID))
	}

	return authorizeResearcherTask(ctx, taskID)
}

// PATCH /quest/tasks/{id}/handoff
func handleUpdateTaskHandoff(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	taskID, err := strconv.Atoi(idStr)
	if err != nil {
		writeError(w, 400, "Invalid task ID")
		return
	}
	var body struct {
		ScheduledEndAt     *string `json:"scheduled_end_at"`
		UserEndAt          *string `json:"user_end_at"`
		HandoffLeadSeconds *int    `json:"handoff_lead_seconds"`
		ClearEmergency     bool    `json:"clear_emergency"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := authorizeResearcherTask(ctx, taskID); err != nil {
		if errors.Is(err, errUploadTaskNotFound) {
			writeError(w, 404, "Task not found")
		} else {
			writeError(w, 403, "Task access denied")
		}
		return
	}

	sched := parseOptionalTime(body.ScheduledEndAt)
	user := parseOptionalTime(body.UserEndAt)
	clearEmerg := body.ClearEmergency

	_, err = db.Exec(ctx, `
		UPDATE tasks SET
			scheduled_end_at = COALESCE($2, scheduled_end_at),
			user_end_at = COALESCE($3, user_end_at),
			handoff_lead_seconds = COALESCE($4, handoff_lead_seconds),
			emergency_handoff_requested_at = CASE WHEN $5 THEN NULL ELSE emergency_handoff_requested_at END,
			updated_at = NOW()
		WHERE id = $1
	`, taskID, sched, user, body.HandoffLeadSeconds, clearEmerg)
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	writeJSON(w, 200, map[string]string{"message": "Handoff fields updated"})
}

// sharedHandoffTask mirrors shared.HandoffTask for DB scanning in apiserver.
type sharedHandoffTask struct {
	ID                          int
	Status                      string
	TargetRA                    *float64
	TargetDec                   *float64
	MinAltitudeDeg              *float64
	ScheduledEndAt              *time.Time
	UserEndAt                   *time.Time
	HandoffLeadSeconds          *int
	EmergencyHandoffRequestedAt *time.Time
}

func toSharedHandoffTask(t sharedHandoffTask) shared.HandoffTask {
	return shared.HandoffTask{
		ID:                          t.ID,
		Status:                      t.Status,
		TargetRA:                    t.TargetRA,
		TargetDec:                   t.TargetDec,
		MinAltitudeDeg:              t.MinAltitudeDeg,
		ScheduledEndAt:              t.ScheduledEndAt,
		UserEndAt:                   t.UserEndAt,
		HandoffLeadSeconds:          t.HandoffLeadSeconds,
		EmergencyHandoffRequestedAt: t.EmergencyHandoffRequestedAt,
	}
}

// Import shared handoff helpers via thin wrappers to avoid circular naming in handlers.
func anyHandoffBroadcastActive(tasks []sharedHandoffTask, now time.Time) bool {
	ht := make([]shared.HandoffTask, len(tasks))
	for i, t := range tasks {
		ht[i] = toSharedHandoffTask(t)
	}
	return shared.AnyHandoffBroadcastActive(ht, now)
}

func broadcastRecommendedPollSeconds(tasks []sharedHandoffTask, now time.Time) int {
	ht := make([]shared.HandoffTask, len(tasks))
	for i, t := range tasks {
		ht[i] = toSharedHandoffTask(t)
	}
	return shared.BroadcastRecommendedPollSeconds(ht, now)
}

func buildHandoffStatusPayload(t sharedHandoffTask, telescopeID string, safety shared.TelescopeSafety, now time.Time, mask shared.ObstructionMask) shared.HandoffStatusPayload {
	return shared.BuildHandoffStatusPayload(toSharedHandoffTask(t), telescopeID, safety, now, mask)
}

func parseOptionalTime(s *string) *time.Time {
	if s == nil {
		return nil
	}
	if *s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, *s)
	if err != nil {
		return nil
	}
	return &t
}
