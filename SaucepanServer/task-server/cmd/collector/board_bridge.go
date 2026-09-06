package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/saucepan/hotpath/shared/wire"
	"go.uber.org/zap"
)

// startCampaignBoardBridge mirrors the MQTT campaign signal stream
// (/board/campaign/{id}/{node}, #463/#470) into campaign_board_notes — the
// same append log the researcher reads over HTTP (#331-C2). Without it, an
// opaque string emitted by a pier or on-pier code reaches other piers but
// never the researcher.
//
// Fail-open: no PG_DSN means the bridge is simply off (the collector's Redis
// state job is unaffected). A note for an unknown campaign_id is dropped by
// the FK and logged at debug.
func startCampaignBoardBridge(ctx context.Context, client mqtt.Client, log *zap.SugaredLogger) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		log.Info("campaign board bridge disabled (PG_DSN unset)")
		return
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Warnw("campaign board bridge: pg connect failed, bridge disabled", "err", err)
		return
	}

	token := client.Subscribe(wire.SubscribeFilter(wire.TopicCampaignBoard), 1, func(_ mqtt.Client, msg mqtt.Message) {
		campaignID, nodeID, ok := parseCampaignBoardTopic(msg.Topic())
		if !ok || len(msg.Payload()) == 0 {
			return
		}
		var note wire.BoardNote
		if err := json.Unmarshal(msg.Payload(), &note); err != nil {
			return
		}
		// The API writes researcher notes to Postgres before publishing them
		// here. The reserved MQTT identity is for live pier fan-out only; do
		// not mirror it back into the append log and create a duplicate row.
		if !shouldBridgeCampaignBoardNote(nodeID) {
			return
		}
		if err := upsertBridgedNote(ctx, pool, campaignID, nodeID, note); err != nil {
			log.Debugw("campaign board bridge: upsert skipped", "campaign", campaignID, "node", nodeID, "err", err)
		}
	})
	if !waitForSubscription(token, log, "campaign board") {
		pool.Close()
		log.Warn("campaign board bridge: subscribe failed, bridge disabled")
		return
	}
	log.Info("campaign board bridge running (campaign board topic → campaign_board_notes)")
}

// shouldBridgeCampaignBoardNote keeps the API's durable researcher row from
// being inserted a second time when its live MQTT fan-out reaches collector.
// All other identities are pier node IDs and belong in the researcher log.
func shouldBridgeCampaignBoardNote(nodeID string) bool {
	return nodeID != "researcher"
}

// parseCampaignBoardTopic splits /board/campaign/{campaign_id}/{node_id}.
func parseCampaignBoardTopic(topic string) (campaignID, nodeID string, ok bool) {
	rest := strings.TrimPrefix(topic, wire.TopicPrefix(wire.TopicCampaignBoard))
	if rest == topic {
		return "", "", false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func upsertBridgedNote(ctx context.Context, pool *pgxpool.Pool, campaignID, nodeID string, note wire.BoardNote) error {
	eventType := note.EventType
	if eventType == "" {
		eventType = "note"
	}
	payload := string(note.Payload)
	if payload == "" {
		payload = "{}"
	}
	sentAt := note.SentAt
	if sentAt.IsZero() {
		sentAt = time.Now().UTC()
	}
	sourceMessageID := note.MessageID
	if sourceMessageID == "" {
		// Older publishers did not include MessageID. Their timestamp remains a
		// stable deduplication fallback while new publishers get independent
		// identity even when several messages share a timestamp.
		sourceMessageID = sentAt.Format(time.RFC3339Nano)
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := pool.Exec(cctx, `
		INSERT INTO campaign_board_notes (campaign_id, author, event_type, message, payload, source_sent_at, source_message_id)
		VALUES ($1::uuid, $2, $3, $4, $5::jsonb, $6, $7)
		ON CONFLICT (campaign_id, author, source_message_id) WHERE source_message_id IS NOT NULL
		DO UPDATE SET event_type = EXCLUDED.event_type,
		              message    = EXCLUDED.message,
		              payload    = EXCLUDED.payload,
		              source_sent_at = EXCLUDED.source_sent_at
	`, campaignID, nodeID, eventType, note.Message, payload, sentAt, sourceMessageID)
	return err
}
