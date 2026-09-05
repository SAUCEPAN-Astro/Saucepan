package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/saucepan/hotpath/shared/wire"
)

// Campaign board — the researcher-facing side of the campaign message stream
// (#179 / #331-C2). Piers coordinate over the retained MQTT board
// (/board/campaign/{id}/{node}, #463/#470); the SDK holds no MQTT credential,
// so a researcher reads and posts over HTTP here. Both routes require a
// researcher JWT and campaign ownership — there is no separate board token.
// Researcher posts are stored here and, when MQTT_BROKER is configured,
// fanned out to the retained MQTT board as the reserved `researcher` author.

type campaignBoardNote struct {
	ID         string          `json:"id"`
	CampaignID string          `json:"campaign_id"`
	Author     string          `json:"author"`
	EventType  string          `json:"event_type"`
	Message    string          `json:"message"`
	Payload    json.RawMessage `json:"payload,omitempty"`
	CreatedAt  string          `json:"created_at"`
}

// boardOwnerGate confirms the caller holds a researcher JWT and owns the
// campaign in the path. It writes the error response itself and returns
// ok=false when the caller should stop. DB work uses r.Context(), which
// net/http cancels when the request ends.
func boardOwnerGate(w http.ResponseWriter, r *http.Request) (campaignID string, ok bool) {
	campaignID = r.PathValue("id")
	claims := claimsFromContext(r.Context())
	if claims == nil || claims.UserID == "" {
		writeError(w, http.StatusUnauthorized, "Researcher auth required")
		return "", false
	}
	owner, err := campaignOwnerID(r.Context(), campaignID)
	if err != nil || owner == "" {
		writeError(w, http.StatusNotFound, "Campaign not found")
		return "", false
	}
	if owner != claims.UserID {
		writeError(w, http.StatusForbidden, "Not campaign owner")
		return "", false
	}
	return campaignID, true
}

// handlePostCampaignBoardNote appends one researcher note to the campaign
// board. The author is always "researcher"; a pier's node_id can never be
// spoofed through this route.
func handlePostCampaignBoardNote(w http.ResponseWriter, r *http.Request) {
	campaignID, ok := boardOwnerGate(w, r)
	if !ok {
		return
	}

	var req struct {
		EventType string          `json:"event_type"`
		Message   string          `json:"message"`
		Payload   json.RawMessage `json:"payload"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	req.EventType = strings.TrimSpace(req.EventType)
	if req.EventType == "" {
		req.EventType = "note"
	}
	payload := req.Payload
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}

	// Cheap flood guard: one campaign cannot append more than 120 notes/min.
	var recent int
	_ = db.QueryRow(r.Context(), `
		SELECT COUNT(*) FROM campaign_board_notes
		WHERE campaign_id = $1::uuid AND created_at > NOW() - INTERVAL '1 minute'
	`, campaignID).Scan(&recent)
	if recent >= 120 {
		writeError(w, http.StatusTooManyRequests, "Board note rate limit (120/min)")
		return
	}

	var note campaignBoardNote
	var createdAt time.Time
	err := db.QueryRow(r.Context(), `
		INSERT INTO campaign_board_notes (campaign_id, author, event_type, message, payload)
		VALUES ($1::uuid, 'researcher', $2, $3, $4::jsonb)
		RETURNING id::text, campaign_id::text, author, event_type, message, payload, created_at
	`, campaignID, req.EventType, req.Message, string(payload)).Scan(
		&note.ID, &note.CampaignID, &note.Author, &note.EventType, &note.Message, &note.Payload, &createdAt,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Database error")
		return
	}
	note.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
	transport := "database_only"
	if boardPublisher != nil {
		boardNote := wire.BoardNote{
			CampaignID: campaignID,
			NodeID:     "researcher",
			MessageID:  note.ID,
			Message:    note.Message,
			EventType:  note.EventType,
			Payload:    note.Payload,
			SentAt:     createdAt.UTC(),
		}
		if err := boardPublisher.Publish(boardNote); err != nil {
			// The durable HTTP record is already committed. Keep it available to
			// the researcher, but make the missing live fan-out visible in logs.
			log.Printf("campaign board MQTT fan-out failed for campaign %s: %v", campaignID, err)
		} else {
			transport = "mqtt_and_database"
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{"note": note, "transport": transport})
}

// handleGetCampaignBoardNotes returns the campaign board in append order,
// optionally only notes after ?since=<RFC3339> (and ?after_id= to break ties).
func handleGetCampaignBoardNotes(w http.ResponseWriter, r *http.Request) {
	campaignID, ok := boardOwnerGate(w, r)
	if !ok {
		return
	}

	since := strings.TrimSpace(r.URL.Query().Get("since"))
	afterID := strings.TrimSpace(r.URL.Query().Get("after_id"))

	q := `
		SELECT id::text, campaign_id::text, author, event_type, message, payload, created_at
		FROM campaign_board_notes
		WHERE campaign_id = $1::uuid`
	args := []any{campaignID}
	switch {
	case since != "" && afterID != "":
		q += ` AND (created_at > $2 OR (created_at = $2 AND id::text > $3))`
		args = append(args, since, afterID)
	case since != "":
		q += ` AND created_at > $2`
		args = append(args, since)
	case afterID != "":
		q += ` AND id::text > $2`
		args = append(args, afterID)
	}
	q += ` ORDER BY created_at ASC, id ASC LIMIT 500`

	rows, err := db.Query(r.Context(), q, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Database error")
		return
	}
	defer rows.Close()

	notes := []campaignBoardNote{}
	for rows.Next() {
		var n campaignBoardNote
		var createdAt time.Time
		if err := rows.Scan(&n.ID, &n.CampaignID, &n.Author, &n.EventType, &n.Message, &n.Payload, &createdAt); err != nil {
			writeError(w, http.StatusInternalServerError, "Database error")
			return
		}
		n.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
		notes = append(notes, n)
	}
	writeJSON(w, http.StatusOK, map[string]any{"notes": notes})
}
