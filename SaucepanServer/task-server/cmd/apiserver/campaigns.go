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

	"github.com/saucepan/hotpath/shared/campaign"
)

func campaignCtx(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, 10*time.Second)
}

func handleCreateCampaign(w http.ResponseWriter, r *http.Request) {
	body, err := readBodyJSON(r)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	pack, packJSON, err := campaign.CanonicalPackJSON(body)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if strings.TrimSpace(pack.Name) == "" {
		writeError(w, 400, "name is required")
		return
	}
	compStarsJSON, err := json.Marshal(pack.CompStars)
	if err != nil {
		writeError(w, 400, "invalid comp_stars")
		return
	}
	if pack.CompStars == nil {
		compStarsJSON = []byte("[]")
	}

	claims := claimsFromContext(r.Context())
	if claims == nil || claims.UserID == "" {
		writeError(w, 401, "authentication required")
		return
	}

	ctx, cancel := campaignCtx(r.Context())
	defer cancel()

	var id string
	err = db.QueryRow(ctx, `
		INSERT INTO campaigns (
			name, description, status, created_by, points_multiplier, test_only, pack_json, comp_stars
		) VALUES ($1, $2, 'draft', $3, 1.0, $4, $5::jsonb, $6::jsonb)
		RETURNING id::text
	`, pack.Name, pack.Description, claims.UserID, pack.TestOnly, string(packJSON), string(compStarsJSON)).Scan(&id)
	if err != nil {
		log.Printf("create campaign: %v", err)
		writeError(w, 500, "Database error")
		return
	}
	writeJSON(w, 201, map[string]interface{}{
		"campaign": map[string]interface{}{"id": id, "status": "draft"},
	})
}

func handleListCampaigns(w http.ResponseWriter, r *http.Request) {
	claims, ok := mustClaims(w, r, "authentication required")
	if !ok {
		return
	}
	status := r.URL.Query().Get("status")
	ctx, cancel := campaignCtx(r.Context())
	defer cancel()

	query := `
		SELECT id::text, name, description, status, created_by::text,
		       points_multiplier, test_only, pack_json, comp_stars, created_at, expanded_at
		FROM campaigns
		WHERE created_by = $1::uuid
	`
	args := []interface{}{claims.UserID}
	if status != "" {
		query += ` AND status = $2`
		args = append(args, status)
	}
	query += ` ORDER BY created_at DESC LIMIT 200`

	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		writeError(w, 500, "Database error")
		return
	}
	defer rows.Close()

	var list []Campaign
	for rows.Next() {
		c, err := scanCampaignRow(rows)
		if err != nil {
			writeError(w, 500, "Database error")
			return
		}
		list = append(list, c)
	}
	if list == nil {
		list = []Campaign{}
	}
	writeJSON(w, 200, map[string]interface{}{"campaigns": list})
}

func handleGetCampaign(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx, cancel := campaignCtx(r.Context())
	defer cancel()

	claims, ok := mustClaims(w, r, "authentication required")
	if !ok {
		return
	}
	if err := assertCampaignOwner(ctx, id, claims.UserID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, 404, "Campaign not found")
			return
		}
		writeError(w, 500, "Database error")
		return
	}

	c, err := loadCampaign(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, 404, "Campaign not found")
			return
		}
		writeError(w, 500, "Database error")
		return
	}
	writeJSON(w, 200, map[string]interface{}{"campaign": c})
}

func handlePublishCampaign(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx, cancel := campaignCtx(r.Context())
	defer cancel()

	claims, ok := mustClaims(w, r, "authentication required")
	if !ok {
		return
	}
	if err := assertCampaignOwner(ctx, id, claims.UserID); err != nil {
		writeError(w, 403, "Forbidden")
		return
	}

	tx, err := db.Begin(ctx)
	if err != nil {
		writeError(w, 500, "Database error")
		return
	}
	defer tx.Rollback(ctx)

	var status string
	var packJSON []byte
	var testOnly bool
	var expandedAt *time.Time
	var hookApproved bool
	err = tx.QueryRow(ctx, `
		SELECT status, pack_json, test_only, expanded_at, hook_approved
		FROM campaigns WHERE id = $1::uuid FOR UPDATE
	`, id).Scan(&status, &packJSON, &testOnly, &expandedAt, &hookApproved)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, 404, "Campaign not found")
			return
		}
		writeError(w, 500, "Database error")
		return
	}
	if status == "archived" {
		writeError(w, 409, "Cannot publish archived campaign")
		return
	}

	var tasksCreated int
	if expandedAt == nil {
		if err := campaign.ValidateStoredPackJSON(packJSON); err != nil {
			writeError(w, 400, err.Error())
			return
		}
		pack, err := campaign.ParsePack(packJSON)
		if err != nil {
			writeError(w, 400, err.Error())
			return
		}
		pack.TestOnly = testOnly
		if err := campaign.ValidateHookPublish(pack, hookApproved); err != nil {
			if errors.Is(err, campaign.ErrHookNotApproved) {
				writeError(w, 403, err.Error())
				return
			}
			writeError(w, 400, err.Error())
			return
		}
		specs, err := campaign.ExpandPack(pack)
		if err != nil {
			writeError(w, 400, err.Error())
			return
		}
		for _, spec := range specs {
			if err := insertCampaignTask(ctx, tx, id, spec); err != nil {
				log.Printf("insert campaign task: %v", err)
				writeError(w, 500, "Database error")
				return
			}
			tasksCreated++
		}
		_, err = tx.Exec(ctx, `
			UPDATE campaigns SET expanded_at = NOW() WHERE id = $1::uuid
		`, id)
		if err != nil {
			writeError(w, 500, "Database error")
			return
		}
	}

	_, err = tx.Exec(ctx, `
		UPDATE campaigns SET status = 'active' WHERE id = $1::uuid
	`, id)
	if err != nil {
		writeError(w, 500, "Database error")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, 500, "Database error")
		return
	}
	emitCampaignUpdate(ctx, id, "campaign.published", "Campaign published and tasks expanded", nil, map[string]any{
		"tasks_created": tasksCreated,
	})
	writeJSON(w, 200, map[string]interface{}{
		"campaign": map[string]interface{}{
			"id":            id,
			"status":        "active",
			"tasks_created": tasksCreated,
		},
	})
}

func handleListCampaignTasks(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx, cancel := campaignCtx(r.Context())
	defer cancel()

	claims, ok := mustClaims(w, r, "authentication required")
	if !ok {
		return
	}
	if err := assertCampaignOwner(ctx, id, claims.UserID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, 404, "Campaign not found")
			return
		}
		writeError(w, 500, "Database error")
		return
	}

	rows, err := db.Query(ctx, `
		SELECT id, public_id::text, name, priority, status, integration_time,
		       normalized_integration_budget_s, normalized_integration_earned_s,
		       target_ra, target_dec, assigned_telescope_id
		FROM tasks WHERE campaign_id = $1::uuid
		ORDER BY id
	`, id)
	if err != nil {
		writeError(w, 500, "Database error")
		return
	}
	defer rows.Close()

	var tasks []Task
	for rows.Next() {
		var task Task
		err := rows.Scan(
			&task.InternalID, &task.PublicID, &task.Name, &task.Priority, &task.Status,
			&task.IntegrationTime, &task.NormalizedIntegrationBudgetS, &task.NormalizedIntegrationEarnedS,
			&task.TargetRA, &task.TargetDec, &task.AssignedTelescopeID,
		)
		if err != nil {
			writeError(w, 500, "Database error")
			return
		}
		task.CampaignID = id
		tasks = append(tasks, task)
	}
	if tasks == nil {
		tasks = []Task{}
	}
	writeJSON(w, 200, map[string]interface{}{"tasks": tasks})
}

// GET /api/v1/campaigns/{id}/stack-status — stack-eligible frame counts per task.
func handleCampaignStackStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx, cancel := campaignCtx(r.Context())
	defer cancel()

	claims, ok := mustClaims(w, r, "authentication required")
	if !ok {
		return
	}
	if err := assertCampaignOwner(ctx, id, claims.UserID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, 404, "Campaign not found")
			return
		}
		writeError(w, 500, "Database error")
		return
	}

	rows, err := db.Query(ctx, `
		SELECT t.public_id::text, t.name, t.status,
		       COUNT(fg.id)::int AS frame_count,
		       COUNT(*) FILTER (WHERE fg.stack_eligible = true)::int AS eligible_frames
		FROM tasks t
		LEFT JOIN frame_grades fg ON fg.task_id = t.id
		WHERE t.campaign_id = $1::uuid
		GROUP BY t.id, t.public_id, t.name, t.status
		ORDER BY t.id
	`, id)
	if err != nil {
		writeError(w, 500, "Database error")
		return
	}
	defer rows.Close()

	tasks := []map[string]any{}
	for rows.Next() {
		var publicID, name, status string
		var frameCount, eligible int
		if err := rows.Scan(&publicID, &name, &status, &frameCount, &eligible); err != nil {
			writeError(w, 500, "Database error")
			return
		}
		tasks = append(tasks, map[string]any{
			"task_id":         publicID,
			"name":            name,
			"status":          status,
			"frame_count":     frameCount,
			"eligible_frames": eligible,
		})
	}
	writeJSON(w, 200, map[string]any{
		"campaign_id": id,
		"tasks":       tasks,
	})
}

func handleAddCampaignTask(w http.ResponseWriter, r *http.Request) {
	campaignID := r.PathValue("id")
	var task Task
	if err := decodeJSON(r, &task); err != nil {
		writeError(w, 400, "Invalid JSON: "+err.Error())
		return
	}
	if task.Name == "" {
		writeError(w, 400, "name is required")
		return
	}
	if task.NormalizedIntegrationBudgetS <= 0 {
		writeError(w, 400, "normalized_integration_budget_s is required")
		return
	}

	ctx, cancel := campaignCtx(r.Context())
	defer cancel()

	claims, ok := mustClaims(w, r, "authentication required")
	if !ok {
		return
	}
	if err := assertCampaignOwner(ctx, campaignID, claims.UserID); err != nil {
		writeError(w, 403, "Forbidden")
		return
	}

	var exists bool
	err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM campaigns WHERE id = $1::uuid)`, campaignID).Scan(&exists)
	if err != nil || !exists {
		writeError(w, 404, "Campaign not found")
		return
	}

	spec := campaign.TaskSpec{
		Name:                         task.Name,
		TargetRA:                     task.TargetRA,
		TargetDec:                    task.TargetDec,
		RequiredFilters:              task.RequiredFilters,
		IntegrationTime:              task.IntegrationTime,
		NormalizedIntegrationBudgetS: task.NormalizedIntegrationBudgetS,
		AllowEmulator:                task.AllowEmulator,
		ProductMode:                  task.ProductMode,
		TargetMagnitude:              task.TargetMagnitude,
	}
	var publicID string
	err = insertCampaignTaskReturning(ctx, db, campaignID, spec, task.Priority, &publicID)
	if err != nil {
		log.Printf("add campaign task: %v", err)
		writeError(w, 500, "Database error")
		return
	}
	writeJSON(w, 201, map[string]interface{}{
		"task": map[string]interface{}{"id": publicID, "campaign_id": campaignID},
	})
}

func handlePauseCampaign(w http.ResponseWriter, r *http.Request) {
	transitionCampaignStatus(w, r, campaign.StatusPaused, campaign.CanPause)
}

func handleResumeCampaign(w http.ResponseWriter, r *http.Request) {
	transitionCampaignStatus(w, r, campaign.StatusActive, campaign.CanResume)
}

func transitionCampaignStatus(
	w http.ResponseWriter,
	r *http.Request,
	targetStatus string,
	allowed func(string) bool,
) {
	id := r.PathValue("id")
	ctx, cancel := campaignCtx(r.Context())
	defer cancel()

	claims, ok := mustClaims(w, r, "authentication required")
	if !ok {
		return
	}
	if err := assertCampaignOwner(ctx, id, claims.UserID); err != nil {
		writeError(w, 403, "Forbidden")
		return
	}

	var status string
	err := db.QueryRow(ctx, `SELECT status FROM campaigns WHERE id = $1::uuid`, id).Scan(&status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, 404, "Campaign not found")
			return
		}
		writeError(w, 500, "Database error")
		return
	}
	if !allowed(status) {
		writeError(w, 409, fmt.Sprintf("cannot transition from %q to %q", status, targetStatus))
		return
	}
	_, err = db.Exec(ctx, `UPDATE campaigns SET status = $1 WHERE id = $2::uuid`, targetStatus, id)
	if err != nil {
		writeError(w, 500, "Database error")
		return
	}
	writeJSON(w, 200, map[string]interface{}{
		"campaign": map[string]interface{}{"id": id, "status": targetStatus},
	})
}

// CampaignLeaderboardEntry is one telescope row in a campaign points leaderboard.
type CampaignLeaderboardEntry struct {
	TelescopeID string  `json:"telescope_id"`
	TotalPoints float64 `json:"total_points"`
	FrameCount  int     `json:"frame_count"`
}

// GET /api/v1/campaigns/{id}/leaderboard — cumulative points by telescope for campaign tasks.
func handleCampaignLeaderboard(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 100 {
		limit = 100
	}

	ctx, cancel := campaignCtx(r.Context())
	defer cancel()

	claims, ok := mustClaims(w, r, "authentication required")
	if !ok {
		return
	}
	if err := assertCampaignOwner(ctx, id, claims.UserID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, 404, "Campaign not found")
			return
		}
		writeError(w, 500, "Database error")
		return
	}

	rows, err := db.Query(ctx, `
		SELECT fg.telescope_id,
		       COALESCE(SUM(fg.points_earned), 0) AS total_points,
		       COUNT(*)::int AS frame_count
		FROM frame_grades fg
		JOIN tasks t ON fg.task_id = t.id
		WHERE t.campaign_id = $1::uuid
		GROUP BY fg.telescope_id
		ORDER BY total_points DESC, fg.telescope_id ASC
		LIMIT $2
	`, id, limit)
	if err != nil {
		writeError(w, 500, "Database error")
		return
	}
	defer rows.Close()

	entries := []CampaignLeaderboardEntry{}
	for rows.Next() {
		var e CampaignLeaderboardEntry
		if err := rows.Scan(&e.TelescopeID, &e.TotalPoints, &e.FrameCount); err != nil {
			writeError(w, 500, "Database error")
			return
		}
		entries = append(entries, e)
	}
	writeJSON(w, 200, map[string]interface{}{
		"campaign_id": id,
		"entries":     entries,
	})
}
