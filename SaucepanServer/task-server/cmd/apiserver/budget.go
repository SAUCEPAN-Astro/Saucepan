package main

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/saucepan/hotpath/shared/chw"
)

// applyTaskBudgetDebit credits normalized integration time after a successful grade.
// No-op when stackEligible is false, exptime <= 0, or taskID is nil.
func applyTaskBudgetDebit(ctx context.Context, taskID *int, telescopeID string, exptime float64, stackEligible bool) error {
	if taskID == nil || *taskID == 0 || !stackEligible || exptime <= 0 {
		return nil
	}

	var apertureMM, qe *float64
	err := db.QueryRow(ctx,
		`SELECT aperture_mm, qe FROM telescopes WHERE telescope_id = $1`,
		telescopeID,
	).Scan(&apertureMM, &qe)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil
		}
		return fmt.Errorf("telescope for budget debit: %w", err)
	}

	qeVal := 0.0
	if qe != nil {
		qeVal = *qe
	}
	apertureVal := 0.0
	if apertureMM != nil {
		apertureVal = *apertureMM
	}
	delta := exptime * chw.CHw(apertureVal, qeVal)
	if delta <= 0 {
		return nil
	}

	var budget *float64
	var earned float64
	err = db.QueryRow(ctx,
		`SELECT normalized_integration_budget_s, normalized_integration_earned_s
		 FROM tasks WHERE id = $1`,
		*taskID,
	).Scan(&budget, &earned)
	if err != nil {
		return fmt.Errorf("task budget read: %w", err)
	}

	newEarned := earned + delta
	_, err = db.Exec(ctx,
		`UPDATE tasks SET normalized_integration_earned_s = $1, updated_at = NOW() WHERE id = $2`,
		newEarned, *taskID,
	)
	if err != nil {
		return fmt.Errorf("task budget update: %w", err)
	}

	if budget != nil && *budget > 0 && newEarned >= *budget {
		tag, err := db.Exec(ctx,
			`UPDATE tasks SET status = 'completed', updated_at = NOW()
			 WHERE id = $1 AND status NOT IN ('completed', 'superseded')`,
			*taskID,
		)
		if err != nil {
			return fmt.Errorf("task complete: %w", err)
		}
		if tag.RowsAffected() > 0 {
			emitTaskBudgetComplete(ctx, *taskID)
			maybeCompleteCampaign(ctx, *taskID)
		}
	}
	return nil
}

func emitTaskBudgetComplete(ctx context.Context, taskID int) {
	var campaignID *string
	_ = db.QueryRow(ctx, `SELECT campaign_id::text FROM tasks WHERE id = $1`, taskID).Scan(&campaignID)
	if campaignID == nil || *campaignID == "" {
		return
	}
	tid := taskID
	emitCampaignUpdate(ctx, *campaignID, "task.budget_complete", "Task normalized integration budget reached", &tid, nil)
}

// maybeCompleteCampaign sets campaign status=completed when all tasks are done.
func maybeCompleteCampaign(ctx context.Context, taskID int) {
	var campaignID *string
	err := db.QueryRow(ctx, `SELECT campaign_id::text FROM tasks WHERE id = $1`, taskID).Scan(&campaignID)
	if err != nil || campaignID == nil || *campaignID == "" {
		return
	}

	var remaining int
	err = db.QueryRow(ctx, `
		SELECT COUNT(*)::int FROM tasks
		WHERE campaign_id = $1::uuid
		  AND status NOT IN ('completed', 'superseded')
	`, *campaignID).Scan(&remaining)
	if err != nil || remaining > 0 {
		return
	}

	tag, err := db.Exec(ctx, `
		UPDATE campaigns SET status = 'completed'
		WHERE id = $1::uuid AND status = 'active'
	`, *campaignID)
	if err != nil || tag.RowsAffected() == 0 {
		return
	}
	emitCampaignUpdate(ctx, *campaignID, "campaign.completed", "All campaign task budgets earned", nil, nil)
}
