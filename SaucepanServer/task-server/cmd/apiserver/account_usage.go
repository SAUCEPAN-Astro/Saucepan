package main

import (
	"net/http"
	"time"
)

// GET /api/v1/account/usage — researcher account usage derived from frame_grades (#423).
//
// Aggregates graded frames on campaigns owned by the JWT user (campaigns.created_by).
// This is a tracker, not a hard publish quota gate.
func handleAccountUsage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := campaignCtx(r.Context())
	defer cancel()

	claims := claimsFromContext(r.Context())
	if claims == nil || claims.UserID == "" {
		writeError(w, 401, "authentication required")
		return
	}
	userID := claims.UserID

	var since, until *time.Time
	if s := r.URL.Query().Get("since"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			writeError(w, 400, "since must be RFC3339")
			return
		}
		since = &t
	}
	if s := r.URL.Query().Get("until"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			writeError(w, 400, "until must be RFC3339")
			return
		}
		until = &t
	}

	// Totals from grades on owned campaigns.
	row := db.QueryRow(ctx, `
		SELECT
			COUNT(*)::bigint,
			COUNT(*) FILTER (WHERE COALESCE(fg.stack_eligible, false))::bigint,
			COALESCE(SUM(fg.points_earned), 0),
			COALESCE(SUM(COALESCE(fg.sp_exptime, 0)), 0)
		FROM frame_grades fg
		JOIN tasks t ON t.id = fg.task_id
		JOIN campaigns c ON c.id = t.campaign_id
		WHERE c.created_by = $1::uuid
		  AND ($2::timestamptz IS NULL OR fg.created_at >= $2)
		  AND ($3::timestamptz IS NULL OR fg.created_at <= $3)
	`, userID, since, until)

	var framesTotal, framesEligible int64
	var pointsSum, exptimeSum float64
	if err := row.Scan(&framesTotal, &framesEligible, &pointsSum, &exptimeSum); err != nil {
		writeError(w, 500, "Database error")
		return
	}

	// Budget progress across owned tasks (current earned/budget, not time-filtered).
	var budgetS, earnedS float64
	var campaignCount, taskCount int
	err := db.QueryRow(ctx, `
		SELECT
			COUNT(DISTINCT c.id)::int,
			COUNT(t.id)::int,
			COALESCE(SUM(COALESCE(t.normalized_integration_budget_s, 0)), 0),
			COALESCE(SUM(COALESCE(t.normalized_integration_earned_s, 0)), 0)
		FROM campaigns c
		LEFT JOIN tasks t ON t.campaign_id = c.id
		WHERE c.created_by = $1::uuid
	`, userID).Scan(&campaignCount, &taskCount, &budgetS, &earnedS)
	if err != nil {
		writeError(w, 500, "Database error")
		return
	}

	rows, err := db.Query(ctx, `
		SELECT
			c.id::text,
			c.name,
			c.status,
			COUNT(fg.id)::bigint,
			COUNT(fg.id) FILTER (WHERE COALESCE(fg.stack_eligible, false))::bigint,
			COALESCE(SUM(fg.points_earned), 0),
			COALESCE(SUM(COALESCE(fg.sp_exptime, 0)), 0)
		FROM campaigns c
		LEFT JOIN tasks t ON t.campaign_id = c.id
		LEFT JOIN frame_grades fg ON fg.task_id = t.id
			AND ($2::timestamptz IS NULL OR fg.created_at >= $2)
			AND ($3::timestamptz IS NULL OR fg.created_at <= $3)
		WHERE c.created_by = $1::uuid
		GROUP BY c.id, c.name, c.status
		ORDER BY c.name
	`, userID, since, until)
	if err != nil {
		writeError(w, 500, "Database error")
		return
	}
	defer rows.Close()

	campaigns := make([]map[string]any, 0)
	for rows.Next() {
		var id, name, status string
		var nFrames, nElig int64
		var pts, exp float64
		if err := rows.Scan(&id, &name, &status, &nFrames, &nElig, &pts, &exp); err != nil {
			writeError(w, 500, "Database error")
			return
		}
		var cBudget, cEarned float64
		_ = db.QueryRow(ctx, `
			SELECT
				COALESCE(SUM(COALESCE(normalized_integration_budget_s, 0)), 0),
				COALESCE(SUM(COALESCE(normalized_integration_earned_s, 0)), 0)
			FROM tasks WHERE campaign_id = $1::uuid
		`, id).Scan(&cBudget, &cEarned)

		campaigns = append(campaigns, map[string]any{
			"campaign_id":                     id,
			"name":                            name,
			"status":                          status,
			"frames_graded":                   nFrames,
			"frames_stack_eligible":           nElig,
			"points_earned":                   pts,
			"sp_exptime_s":                    exp,
			"normalized_integration_budget_s": cBudget,
			"normalized_integration_earned_s": cEarned,
		})
	}

	budgetFrac := 0.0
	if budgetS > 0 {
		budgetFrac = earnedS / budgetS
	}

	writeJSON(w, 200, map[string]any{
		"user_id": userID,
		"window": map[string]any{
			"since": since,
			"until": until,
		},
		"totals": map[string]any{
			"campaigns":                       campaignCount,
			"tasks":                           taskCount,
			"frames_graded":                   framesTotal,
			"frames_stack_eligible":           framesEligible,
			"points_earned":                   pointsSum,
			"sp_exptime_s":                    exptimeSum,
			"normalized_integration_budget_s": budgetS,
			"normalized_integration_earned_s": earnedS,
			"normalized_integration_fraction": budgetFrac,
		},
		"campaigns": campaigns,
		"note":      "Derived from frame_grades on campaigns you own. Tracker only — does not block publish. Points are pier rewards for graded frames; sp_exptime_s and normalized_integration_* measure network resource consumed.",
	})
}
