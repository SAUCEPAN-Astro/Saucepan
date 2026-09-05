package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	devScopeTasksWrite = "tasks:write"
	devScopeTasksRead  = "tasks:read"
	devScopeStatusRead = "status:read"

	developerQuotaTotal = 1000
)

var allowedAPIScopes = map[string]bool{
	devScopeTasksWrite: true,
	devScopeTasksRead:  true,
	devScopeStatusRead: true,
}

// Developer API keys: issuance, hashing, scope enforcement, the
// requireAPIKey middleware. Split from the old developer.go (#431).

// sha256Hex returns the hex-encoded SHA-256 digest of s (used for API key/token hashing).
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func validateAPIKeyScopes(scopes []string) error {
	for _, scope := range scopes {
		if !allowedAPIScopes[scope] {
			return fmt.Errorf("unknown scope %q; allowed: tasks:write, tasks:read, status:read", scope)
		}
	}
	return nil
}

type apiKeyAuth struct {
	UserID string
	Scopes map[string]bool
}

var errInvalidAPIKey = errors.New("invalid or revoked API key")

func apiKeyFromRequest(r *http.Request) string {
	if k := strings.TrimSpace(r.Header.Get("X-API-Key")); k != "" {
		return k
	}
	return ""
}

func lookupAPIKey(ctx context.Context, rawKey string) (*apiKeyAuth, error) {
	if !strings.HasPrefix(rawKey, "sp_live_") || len(rawKey) < 20 {
		return nil, errInvalidAPIKey
	}
	hash := sha256Hex(rawKey)

	var userID string
	var scopes []string
	var active bool
	var expiresAt *time.Time
	var approved bool
	var verified bool
	err := db.QueryRow(ctx, `
		SELECT k.user_id::text, k.scopes, k.is_active, k.expires_at,
		       COALESCE(u.researcher_approved, false), COALESCE(u.email_verified, false)
		FROM developer_api_keys k
		JOIN users u ON u.id = k.user_id
		WHERE k.key_hash = $1
	`, hash).Scan(&userID, &scopes, &active, &expiresAt, &approved, &verified)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errInvalidAPIKey
		}
		return nil, err
	}
	if !active {
		return nil, errInvalidAPIKey
	}
	if !approved || !verified {
		// Offboarded / unapproved researchers must not keep key access (#251).
		return nil, errInvalidAPIKey
	}
	if expiresAt != nil && time.Now().After(*expiresAt) {
		return nil, errInvalidAPIKey
	}
	scopeSet := map[string]bool{}
	for _, s := range scopes {
		scopeSet[s] = true
	}
	_, _ = db.Exec(ctx, `UPDATE developer_api_keys SET last_used_at = NOW() WHERE key_hash = $1`, hash)
	return &apiKeyAuth{UserID: userID, Scopes: scopeSet}, nil
}

func requireAPIKey(scopes ...string) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			raw := apiKeyFromRequest(r)
			if raw == "" {
				writeError(w, 401, "API key required")
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()
			auth, err := lookupAPIKey(ctx, raw)
			if err != nil {
				writeError(w, 401, errInvalidAPIKey.Error())
				return
			}
			for _, scope := range scopes {
				if !auth.Scopes[scope] {
					writeError(w, 403, "insufficient scope")
					return
				}
			}
			next(w, r.WithContext(context.WithValue(r.Context(), apiKeyCtxKey{}, auth)))
		}
	}
}

type apiKeyCtxKey struct{}

func apiKeyFromContext(ctx context.Context) *apiKeyAuth {
	v, _ := ctx.Value(apiKeyCtxKey{}).(*apiKeyAuth)
	return v
}

func handleListAPIKeys(w http.ResponseWriter, r *http.Request) {
	claims, ok := mustClaims(w, r, "authentication required")
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := db.Query(ctx, `
		SELECT id, name, key_prefix, scopes, is_active, last_used_at, expires_at, created_at
		FROM developer_api_keys WHERE user_id = $1::uuid ORDER BY created_at DESC
	`, claims.UserID)
	if err != nil {
		writeError(w, 500, "Database error")
		return
	}
	defer rows.Close()

	keys := []map[string]any{}
	for rows.Next() {
		var id int
		var name, prefix string
		var scopes []string
		var active bool
		var lastUsed, expires, created *time.Time
		if err := rows.Scan(&id, &name, &prefix, &scopes, &active, &lastUsed, &expires, &created); err != nil {
			writeError(w, 500, "Database error")
			return
		}
		row := map[string]any{
			"id": id, "name": name, "key_prefix": prefix, "scopes": scopes, "is_active": active,
		}
		if lastUsed != nil {
			row["last_used_at"] = lastUsed.UTC().Format(time.RFC3339)
		}
		if expires != nil {
			row["expires_at"] = expires.UTC().Format(time.RFC3339)
		}
		if created != nil {
			row["created_at"] = created.UTC().Format(time.RFC3339)
		}
		keys = append(keys, row)
	}
	writeJSON(w, 200, keys)
}

func handleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	claims, ok := mustClaims(w, r, "authentication required")
	if !ok {
		return
	}
	var body struct {
		Name          string   `json:"name"`
		Scopes        []string `json:"scopes"`
		ExpiresInDays *int     `json:"expires_in_days"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, "Invalid JSON")
		return
	}
	if strings.TrimSpace(body.Name) == "" || len(body.Scopes) == 0 {
		writeError(w, 400, "name and scopes are required")
		return
	}
	if err := validateAPIKeyScopes(body.Scopes); err != nil {
		writeError(w, 400, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	var approved bool
	err := db.QueryRow(ctx, `
		SELECT researcher_approved FROM users WHERE id = $1
	`, claims.UserID).Scan(&approved)
	if err != nil {
		writeError(w, 500, "Database error")
		return
	}
	if !approved {
		writeError(w, 403, errResearcherNotApproved.Error())
		return
	}

	secret, prefix, hash, err := generateAPIKeyMaterial()
	if err != nil {
		writeError(w, 500, "Internal error")
		return
	}
	var expiresAt *time.Time
	if body.ExpiresInDays != nil && *body.ExpiresInDays > 0 {
		t := time.Now().Add(time.Duration(*body.ExpiresInDays) * 24 * time.Hour)
		expiresAt = &t
	}

	var id int
	err = db.QueryRow(ctx, `
		INSERT INTO developer_api_keys (user_id, name, key_prefix, key_hash, scopes, expires_at)
		VALUES ($1::uuid, $2, $3, $4, $5, $6)
		RETURNING id
	`, claims.UserID, body.Name, prefix, hash, body.Scopes, expiresAt).Scan(&id)
	if err != nil {
		writeError(w, 500, "Database error")
		return
	}
	writeJSON(w, 201, map[string]any{
		"id": id, "name": body.Name, "key_prefix": prefix, "scopes": body.Scopes,
		"is_active": true, "key": secret,
	})
}

func handleRevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	claims, ok := mustClaims(w, r, "authentication required")
	if !ok {
		return
	}
	keyID := r.PathValue("key_id")
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	tag, err := db.Exec(ctx, `
		UPDATE developer_api_keys SET is_active = false
		WHERE id = $1::int AND user_id = $2::uuid
	`, keyID, claims.UserID)
	if err != nil {
		writeError(w, 500, "Database error")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, 404, "Key not found")
		return
	}
	writeJSON(w, 200, map[string]any{"revoked": true})
}

func generateAPIKeyMaterial() (secret, prefix, hash string, err error) {
	buf := make([]byte, 24)
	if _, err = rand.Read(buf); err != nil {
		return
	}
	secret = "sp_live_" + hex.EncodeToString(buf)
	prefix = secret[:12]
	hash = sha256Hex(secret)
	return
}
