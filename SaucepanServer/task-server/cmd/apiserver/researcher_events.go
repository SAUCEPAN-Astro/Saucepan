package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	eventKindAlert  = "alert"
	eventKindUpdate = "update"
)

type ResearcherEvent struct {
	ID         string          `json:"id"`
	Kind       string          `json:"kind"`
	EventType  string          `json:"event_type"`
	Message    string          `json:"message"`
	CampaignID string          `json:"campaign_id,omitempty"`
	TaskID     *int            `json:"task_id,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
	CreatedAt  string          `json:"created_at"`
}

func emitResearcherEvent(
	ctx context.Context,
	userID string,
	kind string,
	eventType string,
	message string,
	campaignID *string,
	taskID *int,
	payload map[string]any,
) error {
	if userID == "" || message == "" {
		return nil
	}
	payloadJSON, _ := json.Marshal(payload)
	if payloadJSON == nil {
		payloadJSON = []byte("{}")
	}
	_, err := db.Exec(ctx, `
		INSERT INTO researcher_events (user_id, kind, event_type, message, campaign_id, task_id, payload)
		VALUES ($1::uuid, $2, $3, $4, $5::uuid, $6, $7::jsonb)
	`, userID, kind, eventType, message, campaignID, taskID, string(payloadJSON))
	return err
}

func emitCampaignUpdate(ctx context.Context, campaignID string, eventType string, message string, taskID *int, payload map[string]any) {
	userID, err := campaignOwnerID(ctx, campaignID)
	if err != nil || userID == "" {
		return
	}
	cid := campaignID
	_ = emitResearcherEvent(ctx, userID, eventKindUpdate, eventType, message, &cid, taskID, payload)
}

func emitCampaignAlert(ctx context.Context, campaignID string, eventType string, message string, taskID *int, payload map[string]any) {
	userID, err := campaignOwnerID(ctx, campaignID)
	if err != nil || userID == "" {
		return
	}
	cid := campaignID
	_ = emitResearcherEvent(ctx, userID, eventKindAlert, eventType, message, &cid, taskID, payload)
}

func campaignOwnerID(ctx context.Context, campaignID string) (string, error) {
	var owner *string
	err := db.QueryRow(ctx, `SELECT created_by::text FROM campaigns WHERE id = $1::uuid`, campaignID).Scan(&owner)
	if err != nil {
		return "", err
	}
	if owner == nil {
		return "", nil
	}
	return *owner, nil
}

func handleListResearcherEvents(kind string) http.HandlerFunc {
	return requireResearcherJWT(func(w http.ResponseWriter, r *http.Request) {
		claims := claimsFromContext(r.Context())
		since := r.URL.Query().Get("since")
		campaignID := r.URL.Query().Get("campaign_id")

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		query := `
			SELECT id::text, kind, event_type, message, campaign_id::text, task_id, payload, created_at
			FROM researcher_events
			WHERE user_id = $1::uuid AND kind = $2 AND acked_at IS NULL
		`
		args := []any{claims.UserID, kind}
		n := 3
		if since != "" {
			query += ` AND created_at > $` + strconv.Itoa(n)
			args = append(args, since)
			n++
		}
		if campaignID != "" {
			query += ` AND campaign_id = $` + strconv.Itoa(n) + `::uuid`
			args = append(args, campaignID)
		}
		query += ` ORDER BY created_at ASC LIMIT 200`

		rows, err := db.Query(ctx, query, args...)
		if err != nil {
			writeError(w, 500, "Database error")
			return
		}
		defer rows.Close()

		responseKey := "alerts"
		if kind == eventKindUpdate {
			responseKey = "updates"
		}
		list := []ResearcherEvent{}
		for rows.Next() {
			ev, err := scanResearcherEvent(rows)
			if err != nil {
				writeError(w, 500, "Database error")
				return
			}
			list = append(list, ev)
		}
		writeJSON(w, 200, map[string]any{responseKey: list})
	})
}

func handleAckResearcherEvent(kind string) http.HandlerFunc {
	return requireResearcherJWT(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		claims := claimsFromContext(r.Context())
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		tag, err := db.Exec(ctx, `
			UPDATE researcher_events
			SET acked_at = NOW()
			WHERE id = $1::uuid AND user_id = $2::uuid AND kind = $3 AND acked_at IS NULL
		`, id, claims.UserID, kind)
		if err != nil {
			writeError(w, 500, "Database error")
			return
		}
		if tag.RowsAffected() == 0 {
			writeError(w, 404, "Event not found")
			return
		}
		writeJSON(w, 200, map[string]any{"acknowledged": true, "id": id})
	})
}

func scanResearcherEvent(row pgx.Row) (ResearcherEvent, error) {
	var ev ResearcherEvent
	var campaignID *string
	var createdAt time.Time
	err := row.Scan(
		&ev.ID, &ev.Kind, &ev.EventType, &ev.Message,
		&campaignID, &ev.TaskID, &ev.Payload, &createdAt,
	)
	if err != nil {
		return ev, err
	}
	if campaignID != nil {
		ev.CampaignID = *campaignID
	}
	ev.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
	return ev, nil
}
