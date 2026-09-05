package main

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func setupResearcherTestDB(t *testing.T) {
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
		CREATE TABLE IF NOT EXISTS users (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			username TEXT,
			email TEXT UNIQUE,
			password_hash TEXT NOT NULL DEFAULT 'x',
			email_verified BOOLEAN NOT NULL DEFAULT true,
			researcher_approved BOOLEAN NOT NULL DEFAULT false,
			researcher_approved_at TIMESTAMPTZ,
			researcher_approved_by TEXT,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
	`
	if _, err := pool.Exec(ctx, schema); err != nil {
		t.Fatalf("schema setup: %v", err)
	}
	_, _ = pool.Exec(ctx, `
		ALTER TABLE users ADD COLUMN IF NOT EXISTS username TEXT;
		ALTER TABLE users ADD COLUMN IF NOT EXISTS researcher_approved BOOLEAN NOT NULL DEFAULT false;
		ALTER TABLE users ADD COLUMN IF NOT EXISTS researcher_approved_at TIMESTAMPTZ;
		ALTER TABLE users ADD COLUMN IF NOT EXISTS researcher_approved_by TEXT;
		ALTER TABLE users ALTER COLUMN email DROP NOT NULL;
		CREATE UNIQUE INDEX IF NOT EXISTS idx_users_username_lower ON users (lower(username));
	`)
	db = pool
	t.Cleanup(func() {
		pool.Close()
		db = nil
	})
}

func insertTestUser(t *testing.T, ctx context.Context, email string, approved bool) string {
	t.Helper()
	username := email
	if i := len(email); i > 0 {
		for j, c := range email {
			if c == '@' {
				username = email[:j]
				break
			}
		}
	}
	var id string
	err := db.QueryRow(ctx, `
		INSERT INTO users (username, email, password_hash, email_verified, researcher_approved)
		VALUES ($1, $2, 'test-hash', true, $3)
		ON CONFLICT (email) DO UPDATE SET
			researcher_approved = EXCLUDED.researcher_approved,
			username = COALESCE(users.username, EXCLUDED.username)
		RETURNING id
	`, username, email, approved).Scan(&id)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return id
}
