package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/google/uuid"
)

func validatePassword(password string) bool {
	return len(password) >= 8
}

// POST /auth/devices  { "label"?, "telescope_id"? } — JWT required
func handleAuthDevicesCreate(w http.ResponseWriter, r *http.Request) {
	claims, ok := mustClaims(w, r, "Unauthorized")
	if !ok {
		return
	}

	var body struct {
		Label       *string `json:"label"`
		TelescopeID *string `json:"telescope_id"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, 400, "Invalid JSON: "+err.Error())
			return
		}
	}

	nodeID := "node-" + uuid.New().String()
	deviceToken, err := generateRandomHex(32)
	if err != nil {
		log.Printf("auth devices token: %v", err)
		writeError(w, 500, "Internal error")
		return
	}
	tokenHash := hashDeviceToken(deviceToken)

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	_, err = db.Exec(ctx, `
		INSERT INTO user_devices (user_id, node_id, device_token_hash, telescope_id, label)
		VALUES ($1, $2, $3, $4, $5)
	`, claims.UserID, nodeID, tokenHash, body.TelescopeID, body.Label)
	if err != nil {
		log.Printf("auth devices create DB: %v", err)
		writeError(w, 500, "Database error")
		return
	}

	// Intentionally do NOT stamp telescopes.owner_user_id here.
	// Ownership is claimed only via authenticated telescope register
	// (handleRegisterTelescope / assertCanClaimTelescope). Fixes #249.

	log.Printf("Auth devices create: user=%s node=%s", claims.UserID, nodeID)
	writeJSON(w, 201, map[string]string{
		"node_id":      nodeID,
		"device_token": deviceToken,
	})
}

// GET /auth/devices — JWT required
func handleAuthDevicesList(w http.ResponseWriter, r *http.Request) {
	claims, ok := mustClaims(w, r, "Unauthorized")
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := db.Query(ctx, `
		SELECT node_id, telescope_id, label, last_seen_at, created_at
		FROM user_devices WHERE user_id = $1 ORDER BY created_at DESC
	`, claims.UserID)
	if err != nil {
		log.Printf("auth devices list DB: %v", err)
		writeError(w, 500, "Database error")
		return
	}
	defer rows.Close()

	type deviceRow struct {
		NodeID      string  `json:"node_id"`
		TelescopeID *string `json:"telescope_id,omitempty"`
		Label       *string `json:"label,omitempty"`
		LastSeenAt  *string `json:"last_seen_at,omitempty"`
		CreatedAt   string  `json:"created_at"`
	}
	var devices []deviceRow
	for rows.Next() {
		var d deviceRow
		var lastSeen *time.Time
		var createdAt time.Time
		if err := rows.Scan(&d.NodeID, &d.TelescopeID, &d.Label, &lastSeen, &createdAt); err != nil {
			log.Printf("auth devices scan: %v", err)
			continue
		}
		d.CreatedAt = createdAt.Format(time.RFC3339)
		if lastSeen != nil {
			s := lastSeen.Format(time.RFC3339)
			d.LastSeenAt = &s
		}
		devices = append(devices, d)
	}

	writeJSON(w, 200, map[string]interface{}{"devices": devices})
}

// DELETE /auth/devices/{node_id} — JWT required
func handleAuthDevicesDelete(w http.ResponseWriter, r *http.Request) {
	claims, ok := mustClaims(w, r, "Unauthorized")
	if !ok {
		return
	}

	nodeID := r.PathValue("node_id")
	if nodeID == "" {
		writeError(w, 400, "node_id is required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	tag, err := db.Exec(ctx, `
		DELETE FROM user_devices WHERE user_id = $1 AND node_id = $2
	`, claims.UserID, nodeID)
	if err != nil {
		log.Printf("auth devices delete DB: %v", err)
		writeError(w, 500, "Database error")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, 404, "Device not found")
		return
	}

	log.Printf("Auth devices delete: user=%s node=%s", claims.UserID, nodeID)
	writeJSON(w, 200, map[string]string{"revoked": nodeID})
}

// POST /auth/heartbeat — extends access token (JWT required).
// Mints a new access token with the same claim shape as user-server (shared JWT_SECRET).
func handleAuthHeartbeat(w http.ResponseWriter, r *http.Request) {
	claims := claimsFromContext(r.Context())
	if claims == nil {
		tokenStr := extractBearerToken(r)
		if tokenStr == "" {
			writeError(w, 401, "Missing Authorization header")
			return
		}
		parsed, err := parseAccessClaims(tokenStr)
		if err != nil {
			writeError(w, 401, "Invalid or expired token")
			return
		}
		claims = parsed
	}

	username := claims.Username
	if username == "" {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		_ = db.QueryRow(ctx, `SELECT COALESCE(username, '') FROM users WHERE id = $1`, claims.UserID).Scan(&username)
	}
	if username == "" {
		username = claims.Email // legacy tokens
	}

	access, expiresAt, err := generateAccessToken(claims.UserID, username)
	if err != nil {
		writeError(w, 500, "Token generation failed")
		return
	}

	writeJSON(w, 200, map[string]interface{}{
		"access_token": access,
		"expires_at":   expiresAt.Format(time.RFC3339),
	})
}

// POST /auth/verify — deprecated (legacy device-secret flow)
func handleAuthVerifyDeprecated(w http.ResponseWriter, r *http.Request) {
	writeError(w, 410, "Deprecated. Use username+password POST /auth/register and /auth/login.")
}

// POST /auth/reset-request — Phase 2
func handleAuthResetRequestDeprecated(w http.ResponseWriter, r *http.Request) {
	writeError(w, 501, "Password reset is Phase 2. Contact an operator if locked out.")
}

// POST /auth/reset — deprecated
func handleAuthResetDeprecated(w http.ResponseWriter, r *http.Request) {
	writeError(w, 410, "Deprecated. Password reset is Phase 2.")
}
