package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// setupMultiAssignTestDB provisions the minimal tasks + task_assignments schema
// (#402) and points the package db pool at it. Skips when Postgres is absent.
func setupMultiAssignTestDB(t *testing.T) {
	t.Helper()
	dsn := os.Getenv("TEST_PG_DSN")
	if dsn == "" {
		dsn = "postgres://postgres:password@localhost:5432/server_db?sslmode=disable"
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("postgres ping failed: %v", err)
	}

	schema := `
		CREATE TABLE IF NOT EXISTS tasks (
			id SERIAL PRIMARY KEY,
			status TEXT NOT NULL DEFAULT 'pending',
			assigned_telescope_id TEXT,
			campaign_id UUID
		);
		CREATE TABLE IF NOT EXISTS task_assignments (
			task_id          INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
			telescope_id     TEXT NOT NULL,
			role             TEXT NOT NULL DEFAULT 'cohort'
			                   CHECK (role IN ('primary', 'cohort')),
			status           TEXT NOT NULL DEFAULT 'assigned'
			                   CHECK (status IN ('assigned', 'in_progress', 'completed', 'expired', 'released')),
			assigned_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			lease_expires_at TIMESTAMPTZ,
			updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (task_id, telescope_id)
		);
	`
	if _, err := pool.Exec(ctx, schema); err != nil {
		pool.Close()
		t.Fatalf("schema setup: %v", err)
	}

	db = pool
	t.Cleanup(func() {
		pool.Close()
		db = nil
	})
}

// TestValidateUploadAssignmentCohort covers an 8-member cohort task: every
// assigned member (primary + cohort 2..8) passes upload auth, and a telescope
// that is not on the task is still rejected (#402).
func TestValidateUploadAssignmentCohort(t *testing.T) {
	setupMultiAssignTestDB(t)
	ctx := context.Background()

	var taskID int64
	if err := db.QueryRow(ctx, `
		INSERT INTO tasks (status, assigned_telescope_id)
		VALUES ('assigned', 'mc-tele-1') RETURNING id
	`).Scan(&taskID); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(context.Background(), `DELETE FROM tasks WHERE id = $1`, taskID)
	})

	members := make([]string, 8)
	for i := range members {
		members[i] = fmt.Sprintf("mc-tele-%d", i+1)
		role := "cohort"
		if i == 0 {
			role = "primary"
		}
		if _, err := db.Exec(ctx, `
			INSERT INTO task_assignments (task_id, telescope_id, role, status)
			VALUES ($1, $2, $3, 'assigned')
		`, taskID, members[i], role); err != nil {
			t.Fatalf("seed assignment %s: %v", members[i], err)
		}
	}

	// Every cohort member — including 2..8 — must pass upload auth.
	for _, m := range members {
		if err := validateUploadAssignment(ctx, m, taskID); err != nil {
			t.Errorf("member %s: expected upload auth to pass, got %v", m, err)
		}
	}

	// A telescope not on the task is still 403.
	if err := validateUploadAssignment(ctx, "mc-tele-outsider", taskID); !errors.Is(err, errUploadTaskNotAssigned) {
		t.Errorf("non-assigned telescope: want errUploadTaskNotAssigned, got %v", err)
	}

	// A released cohort member loses upload auth.
	if _, err := db.Exec(ctx, `
		UPDATE task_assignments SET status = 'released'
		WHERE task_id = $1 AND telescope_id = $2
	`, taskID, members[3]); err != nil {
		t.Fatalf("release member: %v", err)
	}
	if err := validateUploadAssignment(ctx, members[3], taskID); !errors.Is(err, errUploadTaskNotAssigned) {
		t.Errorf("released member: want errUploadTaskNotAssigned, got %v", err)
	}
}

// TestValidateUploadAssignmentPreMigrationFallback confirms a task with no
// task_assignments rows still authorizes via the legacy assigned_telescope_id
// mirror (#402 backward-compat path).
func TestValidateUploadAssignmentPreMigrationFallback(t *testing.T) {
	setupMultiAssignTestDB(t)
	ctx := context.Background()

	var taskID int64
	if err := db.QueryRow(ctx, `
		INSERT INTO tasks (status, assigned_telescope_id)
		VALUES ('assigned', 'mc-legacy-1') RETURNING id
	`).Scan(&taskID); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(context.Background(), `DELETE FROM tasks WHERE id = $1`, taskID)
	})

	if err := validateUploadAssignment(ctx, "mc-legacy-1", taskID); err != nil {
		t.Errorf("legacy primary: expected pass, got %v", err)
	}
	if err := validateUploadAssignment(ctx, "mc-legacy-other", taskID); !errors.Is(err, errUploadTaskNotAssigned) {
		t.Errorf("legacy non-assignee: want errUploadTaskNotAssigned, got %v", err)
	}
}
