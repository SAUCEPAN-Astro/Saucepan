package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Developer task submission + retrieval (the API-key task surface in
// openapi.yaml). Split from developer.go (#431).

type developerTaskSpec struct {
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	IntegrationTime float64  `json:"integration_time"`
	MinPower        float64  `json:"min_power"`
	TargetMagnitude *float64 `json:"target_magnitude,omitempty"`
	RequiredFilters []string `json:"required_filters"`
	MaxPSFFWHM      *float64 `json:"max_psf_fwhm"`
	MaxPlateScale   *float64 `json:"max_plate_scale"`
	MinApertureMM   *float64 `json:"min_aperture_mm"`
	Priority        int      `json:"priority"`
}

func handleDeveloperCreateTask(w http.ResponseWriter, r *http.Request) {
	auth := apiKeyFromContext(r.Context())
	var spec developerTaskSpec
	if err := decodeJSON(r, &spec); err != nil {
		writeError(w, 400, "Invalid JSON")
		return
	}
	if fields := validateDeveloperTaskSpec(&spec); len(fields) > 0 {
		writeJSON(w, 422, map[string]any{"error": "validation_failed", "message": "Invalid task spec", "fields": fields})
		return
	}
	if spec.Priority <= 0 {
		spec.Priority = 10
	}
	if spec.Priority > 50 {
		spec.Priority = 50
	}

	ctx, cancel := taskLookupCtx(r.Context())
	defer cancel()

	var used int
	if err := db.QueryRow(ctx, `
		SELECT COUNT(*)::int FROM tasks WHERE developer_user_id = $1::uuid
	`, auth.UserID).Scan(&used); err != nil {
		writeError(w, 500, "Database error")
		return
	}
	if used >= developerQuotaTotal {
		writeError(w, http.StatusTooManyRequests, "Developer task quota exceeded")
		return
	}

	var internalID int
	var maxPSF, maxPlate, minAperture interface{}
	if spec.MaxPSFFWHM != nil {
		maxPSF = *spec.MaxPSFFWHM
	}
	if spec.MaxPlateScale != nil {
		maxPlate = *spec.MaxPlateScale
	}
	if spec.MinApertureMM != nil {
		minAperture = *spec.MinApertureMM
	}

	specJSON, _ := json.Marshal(spec)
	err := db.QueryRow(ctx, `
		INSERT INTO tasks (
			name, priority, status, integration_time, min_power, target_magnitude,
			required_filters, max_psf_fwhm_arcsec, max_resolution_arcsec, min_aperture_mm,
			developer_user_id, original_spec
		)
		SELECT $1, $2, 'pending', $3, $4, $5, $6, $7, $8, $9, $10::uuid, $11::jsonb
		WHERE (
			SELECT COUNT(*) FROM tasks WHERE developer_user_id = $10::uuid
		) < $12
		RETURNING id
		`, spec.Name, spec.Priority, spec.IntegrationTime, spec.MinPower, spec.TargetMagnitude,
		nullStringSlice(spec.RequiredFilters), maxPSF, maxPlate, minAperture, auth.UserID, string(specJSON),
		developerQuotaTotal,
	).Scan(&internalID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusTooManyRequests, "Developer task quota exceeded")
			return
		}
		log.Printf("developer create task: %v", err)
		writeError(w, 500, "Database error")
		return
	}

	writeJSON(w, 201, developerTaskToJSON(internalID, "pending", spec))
}

func validateDeveloperTaskSpec(spec *developerTaskSpec) map[string]string {
	fields := map[string]string{}
	if strings.TrimSpace(spec.Name) == "" {
		fields["name"] = "required"
	}
	if spec.IntegrationTime <= 0 {
		fields["integration_time"] = "must be > 0"
	}
	if spec.MinPower < 0 || spec.MinPower > 1 {
		fields["min_power"] = "must be between 0.0 and 1.0"
	}
	return fields
}

func handleDeveloperListTasks(w http.ResponseWriter, r *http.Request) {
	auth := apiKeyFromContext(r.Context())
	ctx, cancel := taskLookupCtx(r.Context())
	defer cancel()

	status := r.URL.Query().Get("status")
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if perPage < 1 {
		perPage = 50
	}
	if perPage > 100 {
		perPage = 100
	}
	offset := (page - 1) * perPage

	query := `
		SELECT id, name, status, integration_time, min_power, required_filters,
		       max_psf_fwhm_arcsec, max_resolution_arcsec, min_aperture_mm, priority
		FROM tasks WHERE developer_user_id = $1::uuid
	`
	args := []any{auth.UserID}
	if status != "" {
		query += ` AND status = $2`
		args = append(args, status)
	}
	query += fmt.Sprintf(` ORDER BY id DESC LIMIT %d OFFSET %d`, perPage, offset)

	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		writeError(w, 500, "Database error")
		return
	}
	defer rows.Close()

	tasks := []map[string]any{}
	for rows.Next() {
		t, err := scanDeveloperTaskRow(rows)
		if err != nil {
			writeError(w, 500, "Database error")
			return
		}
		tasks = append(tasks, t)
	}
	writeJSON(w, 200, map[string]any{"tasks": tasks})
}

func handleDeveloperGetTask(w http.ResponseWriter, r *http.Request) {
	auth := apiKeyFromContext(r.Context())
	taskID := r.PathValue("task_id")
	ctx, cancel := taskLookupCtx(r.Context())
	defer cancel()

	t, err := loadDeveloperTask(ctx, taskID, auth.UserID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, 404, "Task not found")
			return
		}
		writeError(w, 500, "Database error")
		return
	}
	writeJSON(w, 200, t)
}

func handleDeveloperTaskDownloadURL(w http.ResponseWriter, r *http.Request) {
	auth := apiKeyFromContext(r.Context())
	taskID := r.PathValue("task_id")
	ctx, cancel := taskLookupCtx(r.Context())
	defer cancel()

	var status string
	err := db.QueryRow(ctx, `
		SELECT status FROM tasks WHERE id = $1 AND developer_user_id = $2::uuid
	`, taskID, auth.UserID).Scan(&status)
	if err != nil {
		writeError(w, 404, "Task not found")
		return
	}
	if status != "completed" {
		writeError(w, 404, "Task not completed")
		return
	}

	var objectKey, bucket string
	err = db.QueryRow(ctx, `
		SELECT COALESCE(graded_object_key, ''), COALESCE(bucket, 'saucepan')
		FROM developer_deliveries
		WHERE task_id = $1 AND user_id = $2::uuid
		ORDER BY created_at DESC LIMIT 1
	`, taskID, auth.UserID).Scan(&objectKey, &bucket)
	if err != nil || objectKey == "" {
		writeError(w, 404, "No FITS available")
		return
	}
	url, err := presignObjectURL(bucket, objectKey, 15*time.Minute)
	if err != nil {
		writeError(w, 500, "Storage error")
		return
	}
	expires := time.Now().Add(15 * time.Minute).UTC().Format(time.RFC3339)
	writeJSON(w, 200, map[string]any{"url": url, "expires_at": expires})
}

func loadDeveloperTask(ctx context.Context, taskID, userID string) (map[string]any, error) {
	row := db.QueryRow(ctx, `
		SELECT id, name, status, integration_time, min_power, required_filters,
		       max_psf_fwhm_arcsec, max_resolution_arcsec, min_aperture_mm, priority
		FROM tasks WHERE id = $1 AND developer_user_id = $2::uuid
	`, taskID, userID)
	return scanDeveloperTaskRow(row)
}

func scanDeveloperTaskRow(row pgx.Row) (map[string]any, error) {
	var id int
	var name, status string
	var integration, minPower float64
	var filters []string
	var maxPSF, maxPlate, minAperture *float64
	var priority int
	err := row.Scan(&id, &name, &status, &integration, &minPower, &filters,
		&maxPSF, &maxPlate, &minAperture, &priority)
	if err != nil {
		return nil, err
	}
	spec := developerTaskSpec{
		Name: name, IntegrationTime: integration, MinPower: minPower,
		RequiredFilters: filters, Priority: priority,
	}
	if maxPSF != nil {
		spec.MaxPSFFWHM = maxPSF
	}
	if maxPlate != nil {
		spec.MaxPlateScale = maxPlate
	}
	if minAperture != nil {
		spec.MinApertureMM = minAperture
	}
	return developerTaskToJSON(id, status, spec), nil
}

func developerTaskToJSON(id int, status string, spec developerTaskSpec) map[string]any {
	out := map[string]any{
		"id":                id,
		"name":              spec.Name,
		"status":            status,
		"integration_time":  spec.IntegrationTime,
		"min_power":         spec.MinPower,
		"priority":          spec.Priority,
		"developer_task_id": id,
	}
	if spec.Description != "" {
		out["description"] = spec.Description
	}
	if len(spec.RequiredFilters) > 0 {
		out["required_filters"] = spec.RequiredFilters
	}
	if spec.MaxPSFFWHM != nil {
		out["max_psf_fwhm"] = *spec.MaxPSFFWHM
	}
	if spec.MaxPlateScale != nil {
		out["max_plate_scale"] = *spec.MaxPlateScale
	}
	if spec.MinApertureMM != nil {
		out["min_aperture_mm"] = *spec.MinApertureMM
	}
	return out
}
