package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

var (
	errUploadTaskNotFound    = errors.New("task not found")
	errUploadTaskNotAssigned = errors.New("telescope is not assigned to this task")
	errUploadTaskTerminal    = errors.New("task is not open for upload")
)

type uploadTaskRow struct {
	Status              string
	AssignedTelescopeID *string
	CampaignID          *string // UUID text from tasks.campaign_id
}

func loadUploadTask(ctx context.Context, taskID int64) (*uploadTaskRow, error) {
	var row uploadTaskRow
	err := db.QueryRow(ctx, `
		SELECT status, assigned_telescope_id, campaign_id::text
		FROM tasks WHERE id = $1
	`, taskID).Scan(&row.Status, &row.AssignedTelescopeID, &row.CampaignID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errUploadTaskNotFound
		}
		return nil, err
	}
	return &row, nil
}

// validateUploadAssignment enforces Postgres assignment SoT (#264).
// Client campaign_id (legacy int64 path key) is not trusted for auth; when the
// task has a campaign UUID we only require the client sent a non-zero campaign
// placeholder if the path still uses int keys — real binding is task+telescope.
func validateUploadAssignment(ctx context.Context, telescopeID string, taskID int64) error {
	if telescopeID == "" {
		return fmt.Errorf("telescope_id is required")
	}
	if taskID <= 0 {
		return errUploadTaskNotFound
	}
	row, err := loadUploadTask(ctx, taskID)
	if err != nil {
		return err
	}
	status := strings.ToLower(strings.TrimSpace(row.Status))
	switch status {
	case "completed", "cancelled", "failed", "expired":
		return errUploadTaskTerminal
	}

	// Multi-assignee auth (#402): every cohort member of the task has a row in
	// task_assignments, so authorize against that join table. Fall back to the
	// legacy tasks.assigned_telescope_id mirror only for pre-migration tasks
	// that have no join rows at all.
	authorized, joinHasRows, err := lookupTaskAssignmentAuth(ctx, telescopeID, taskID)
	if err != nil {
		return err
	}
	if authorized {
		return nil
	}
	if joinHasRows {
		return errUploadTaskNotAssigned
	}
	if row.AssignedTelescopeID == nil || strings.TrimSpace(*row.AssignedTelescopeID) == "" {
		return errUploadTaskNotAssigned
	}
	if *row.AssignedTelescopeID != telescopeID {
		return errUploadTaskNotAssigned
	}
	return nil
}

// lookupTaskAssignmentAuth reports whether this telescope holds an active
// task_assignments row for the task (authorized), and whether the join table
// has any row for the task at all (joinHasRows) — the latter distinguishes a
// pre-migration task from one this telescope simply is not on.
func lookupTaskAssignmentAuth(ctx context.Context, telescopeID string, taskID int64) (authorized, joinHasRows bool, err error) {
	err = db.QueryRow(ctx, `
		SELECT
		  EXISTS (
		    SELECT 1 FROM task_assignments
		    WHERE task_id = $1 AND telescope_id = $2
		      AND status IN ('assigned', 'in_progress')
		  ),
		  EXISTS (
		    SELECT 1 FROM task_assignments WHERE task_id = $1
		  )
	`, taskID, telescopeID).Scan(&authorized, &joinHasRows)
	return
}
