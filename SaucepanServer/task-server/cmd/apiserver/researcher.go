package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

var errResearcherNotApproved = errors.New("researcher approval required")

func isResearcherApproved(ctx context.Context, userID string) (bool, error) {
	if userID == "" {
		return false, errors.New("missing user id")
	}
	if db == nil {
		return false, errors.New("database not configured")
	}
	qCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var approved bool
	err := db.QueryRow(qCtx, `
		SELECT researcher_approved FROM users WHERE id = $1
	`, userID).Scan(&approved)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, fmt.Errorf("user not found")
		}
		return false, err
	}
	return approved, nil
}

// requireApprovedResearcher returns nil when the JWT user is researcher-approved.
func requireApprovedResearcher(ctx context.Context) error {
	claims := claimsFromContext(ctx)
	if claims == nil || claims.UserID == "" {
		return errors.New("authentication required")
	}
	approved, err := isResearcherApproved(ctx, claims.UserID)
	if err != nil {
		return err
	}
	if !approved {
		return errResearcherNotApproved
	}
	return nil
}

// requireResearcherJWT wraps a handler with JWT auth + researcher approval. It
// gates the entire researcher HTTP surface — campaign CRUD, quest task
// create/patch/complete, the delivery inbox, and the alerts/updates feeds —
// so an un-approved account gets a working login (to see its pending state)
// but 403 on every capability route (#470 item 7). The API-key path is gated
// separately: handleCreateAPIKey refuses to mint a key for an un-approved
// user, and lookupAPIKey re-checks researcher_approved on every call so
// revoking approval kills existing keys immediately (#251).
func requireResearcherJWT(next http.HandlerFunc) http.HandlerFunc {
	return requireJWT(func(w http.ResponseWriter, r *http.Request) {
		if err := requireApprovedResearcher(r.Context()); err != nil {
			if errors.Is(err, errResearcherNotApproved) {
				writeError(w, 403, err.Error())
				return
			}
			writeError(w, 401, err.Error())
			return
		}
		next(w, r)
	})
}
