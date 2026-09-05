package main

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
)

// userOwnsTelescope reports whether userID owns the telescope via owner_user_id.
// A user_devices row alone does NOT confer ownership for unowned telescopes
// (Fixes #249 — device registration must not squat ownership).
func userOwnsTelescope(ctx context.Context, userID, telescopeID string) (bool, error) {
	if userID == "" || telescopeID == "" {
		return false, nil
	}
	var owner *string
	err := db.QueryRow(ctx, `
		SELECT owner_user_id::text FROM telescopes WHERE telescope_id = $1
	`, telescopeID).Scan(&owner)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return owner != nil && *owner == userID, nil
}

// assertCanClaimTelescope returns an error if another user already owns the row.
func assertCanClaimTelescope(ctx context.Context, telescopeID, userID string) error {
	var owner *string
	err := db.QueryRow(ctx, `
		SELECT owner_user_id::text FROM telescopes WHERE telescope_id = $1
	`, telescopeID).Scan(&owner)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	if owner != nil && *owner != userID {
		return errForbidden("telescope owned by another user")
	}
	return nil
}

type forbiddenError string

func (e forbiddenError) Error() string { return string(e) }

func errForbidden(msg string) error { return forbiddenError(msg) }

// authorizeResearcherTask limits researcher task mutations to the campaign
// owner or the developer account that created the task. The route middleware
// already checks researcher approval; this helper supplies the resource check.
func authorizeResearcherTask(ctx context.Context, taskID int) error {
	if err := requireApprovedResearcher(ctx); err != nil {
		return err
	}
	claims := claimsFromContext(ctx)
	if claims == nil || claims.UserID == "" {
		return errForbidden("authentication required")
	}
	var exists, allowed bool
	err := db.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM tasks WHERE id = $1),
		       EXISTS (
				SELECT 1
				FROM tasks t
				LEFT JOIN campaigns c ON c.id = t.campaign_id
				WHERE t.id = $1
				  AND (c.created_by = $2::uuid OR t.developer_user_id = $2::uuid)
			)
	`, taskID, claims.UserID).Scan(&exists, &allowed)
	if err != nil {
		return err
	}
	if !exists {
		return errUploadTaskNotFound
	}
	if !allowed {
		return errForbidden("task is not owned by the caller")
	}
	return nil
}

// requireDeviceOrJWT requires a Bearer access JWT or a registered device token.
// Pier onboarding: login (JWT) → POST /auth/devices → POST /quest/telescopes with that JWT.
// Later pier syncs may use the device_token from /auth/devices (same Authorization: Bearer).
func requireDeviceOrJWT(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tokenStr := extractBearerToken(r)
		if tokenStr == "" {
			writeError(w, 401, "Missing Authorization header")
			return
		}
		if claims, err := parseAccessClaims(tokenStr); err == nil {
			ctx := context.WithValue(r.Context(), userClaimsKey, claims)
			next(w, r.WithContext(ctx))
			return
		}
		device, err := lookupDeviceByToken(r.Context(), tokenStr)
		if err != nil {
			writeError(w, 401, "Invalid or expired token")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), uploadDeviceCtxKey, device)))
	}
}
