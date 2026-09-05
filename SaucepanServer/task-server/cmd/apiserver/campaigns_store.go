package main

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/saucepan/hotpath/shared/campaign"
)

// Campaign persistence helpers: row load/scan and task inserts. The dbExecutor /
// dbQuerier interfaces let the same insert run against either the pool (db) or a
// transaction (tx).

func loadCampaign(ctx context.Context, id string) (Campaign, error) {
	row := db.QueryRow(ctx, `
		SELECT id::text, name, description, status, created_by::text,
		       points_multiplier, test_only, pack_json, comp_stars, created_at, expanded_at
		FROM campaigns WHERE id = $1::uuid
	`, id)
	return scanCampaignRow(row)
}

type dbExecutor interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

type dbQuerier interface {
	QueryRow(ctx context.Context, sql string, arguments ...any) pgx.Row
}

func scanCampaignRow(row pgx.Row) (Campaign, error) {
	var c Campaign
	var createdBy *string
	var createdAt time.Time
	var expandedAt *time.Time
	err := row.Scan(
		&c.ID, &c.Name, &c.Description, &c.Status, &createdBy,
		&c.PointsMultiplier, &c.TestOnly, &c.PackJSON, &c.CompStars, &createdAt, &expandedAt,
	)
	if err != nil {
		return c, err
	}
	if createdBy != nil {
		c.CreatedBy = *createdBy
	}
	c.CreatedAt = createdAt.UTC().Format(time.RFC3339)
	if expandedAt != nil {
		s := expandedAt.UTC().Format(time.RFC3339)
		c.ExpandedAt = &s
	}
	return c, nil
}

func insertCampaignTask(ctx context.Context, exec dbExecutor, campaignID string, spec campaign.TaskSpec) error {
	_, err := exec.Exec(ctx, `
		INSERT INTO tasks (
			name, priority, status, integration_time, normalized_integration_budget_s,
			required_filters, target_ra, target_dec, target_magnitude, allow_emulator, product_mode, campaign_id
		) VALUES ($1, 0, 'pending', $2, $3, $4, $5, $6, $7, $8, $9, $10::uuid)
	`, spec.Name, nullFloat(spec.IntegrationTime), spec.NormalizedIntegrationBudgetS,
		spec.RequiredFilters, spec.TargetRA, spec.TargetDec, spec.TargetMagnitude, spec.AllowEmulator,
		campaign.NormalizedProductMode(&campaign.ProductIntent{Mode: spec.ProductMode}), campaignID)
	return err
}

func insertCampaignTaskReturning(ctx context.Context, q dbQuerier, campaignID string, spec campaign.TaskSpec, priority int, publicID *string) error {
	return q.QueryRow(ctx, `
		INSERT INTO tasks (
			name, priority, status, integration_time, normalized_integration_budget_s,
			required_filters, target_ra, target_dec, target_magnitude, allow_emulator, product_mode, campaign_id
		) VALUES ($1, $2, 'pending', $3, $4, $5, $6, $7, $8, $9, $10, $11::uuid)
		RETURNING public_id::text
	`, spec.Name, priority, nullFloat(spec.IntegrationTime), spec.NormalizedIntegrationBudgetS,
		spec.RequiredFilters, spec.TargetRA, spec.TargetDec, spec.TargetMagnitude, spec.AllowEmulator,
		campaign.NormalizedProductMode(&campaign.ProductIntent{Mode: spec.ProductMode}), campaignID,
	).Scan(publicID)
}
