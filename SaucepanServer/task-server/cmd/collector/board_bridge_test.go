package main

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/saucepan/hotpath/shared/wire"
)

func TestParseCampaignBoardTopic(t *testing.T) {
	cases := []struct {
		topic      string
		camp, node string
		ok         bool
	}{
		{"/board/campaign/camp-1/pier_a", "camp-1", "pier_a", true},
		{"/board/campaign/c/n", "c", "n", true},
		{"/board/campaign/only-one", "", "", false},
		{"/board/campaign/c/n/extra", "", "", false},
		{"/board/task-1/pier_a", "", "", false},
		{"/telemetry/pier_a", "", "", false},
	}
	for _, c := range cases {
		campaign, node, ok := parseCampaignBoardTopic(c.topic)
		if ok != c.ok || campaign != c.camp || node != c.node {
			t.Errorf("parseCampaignBoardTopic(%q) = (%q,%q,%v), want (%q,%q,%v)",
				c.topic, campaign, node, ok, c.camp, c.node, c.ok)
		}
	}
}

func TestResearcherBoardIdentityIsNotBridgedBack(t *testing.T) {
	// The apiserver stores researcher notes before publishing them to MQTT.
	// The collector must not insert a second copy when it sees that fan-out.
	if shouldBridgeCampaignBoardNote("researcher") {
		t.Fatal("researcher identity must not be bridged")
	}
	if !shouldBridgeCampaignBoardNote("pier_a") {
		t.Fatal("pier identity must be bridged")
	}
}

// setupBoardBridgeDB provisions the minimal schema the bridge upsert touches.
func setupBoardBridgeDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_PG_DSN")
	if dsn == "" {
		t.Skip("TEST_PG_DSN is required for the board bridge integration test")
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
	_, err = pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS campaigns (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			name TEXT, status TEXT, created_by UUID,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE TABLE IF NOT EXISTS campaign_board_notes (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			campaign_id UUID NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
			author TEXT NOT NULL,
			event_type TEXT NOT NULL DEFAULT 'note',
			message TEXT NOT NULL DEFAULT '',
			payload JSONB NOT NULL DEFAULT '{}'::jsonb,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			source_sent_at TIMESTAMPTZ,
			source_message_id TEXT
		);
		CREATE UNIQUE INDEX IF NOT EXISTS ux_campaign_board_notes_message_id
			ON campaign_board_notes (campaign_id, author, source_message_id)
			WHERE source_message_id IS NOT NULL;
	`)
	if err != nil {
		pool.Close()
		t.Fatalf("schema: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestUpsertBridgedNoteDedupsByMessageID(t *testing.T) {
	pool := setupBoardBridgeDB(t)
	ctx := context.Background()

	var campaignID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO campaigns (name, status) VALUES ('c', 'active') RETURNING id::text`,
	).Scan(&campaignID); err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM campaigns WHERE id = $1::uuid`, campaignID)
	})

	t0 := time.Now().UTC().Truncate(time.Millisecond)
	note := wire.BoardNote{
		NodeID: "pier_a", MessageID: "m1", Message: "tile 3 clear", EventType: "note", SentAt: t0,
	}

	// Same retained signal delivered twice -> one row, updated in place.
	if err := upsertBridgedNote(ctx, pool, campaignID, "pier_a", note); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	note.Message = "tile 3 clear (revised)"
	if err := upsertBridgedNote(ctx, pool, campaignID, "pier_a", note); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	// A newer note from the same pier -> a distinct row.
	note2 := wire.BoardNote{
		NodeID: "pier_a", MessageID: "m2", Message: "need more time", EventType: "request_time",
		Payload: json.RawMessage(`{"seconds":600}`), SentAt: t0,
	}
	if err := upsertBridgedNote(ctx, pool, campaignID, "pier_a", note2); err != nil {
		t.Fatalf("third upsert: %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM campaign_board_notes WHERE campaign_id = $1::uuid`, campaignID,
	).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Fatalf("row count = %d, want 2 (one deduped, one new)", count)
	}

	var msg, eventType string
	if err := pool.QueryRow(ctx, `
		SELECT message, event_type FROM campaign_board_notes
		WHERE campaign_id = $1::uuid AND source_message_id = 'm1'
	`, campaignID).Scan(&msg, &eventType); err != nil {
		t.Fatalf("read deduped row: %v", err)
	}
	if msg != "tile 3 clear (revised)" {
		t.Fatalf("deduped row message = %q, want the revised text", msg)
	}

	var author string
	var payload []byte
	if err := pool.QueryRow(ctx, `
		SELECT author, payload::text FROM campaign_board_notes
		WHERE campaign_id = $1::uuid AND event_type = 'request_time'
	`, campaignID).Scan(&author, &payload); err != nil {
		t.Fatalf("read event row: %v", err)
	}
	if author != "pier_a" || string(payload) != `{"seconds": 600}` {
		t.Fatalf("event row author=%q payload=%s", author, payload)
	}
}
