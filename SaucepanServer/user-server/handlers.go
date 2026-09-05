package main

import (
	"errors"
	"log"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func requestUserAgent(r *http.Request) string {
	return r.Header.Get("User-Agent")
}

func issueAndStoreSession(w http.ResponseWriter, r *http.Request, status int, userID, username string) {
	pair, err := generateTokenPair(userID, username)
	if err != nil {
		log.Printf("user-server token: %v", err)
		writeError(w, 500, "Token generation failed")
		return
	}
	ctx, cancel := withTimeout(r)
	defer cancel()
	if _, err := createSession(ctx, userID, pair.RefreshToken, requestUserAgent(r), refreshExpiresAt()); err != nil {
		log.Printf("user-server create session: %v", err)
		writeError(w, 500, "Session creation failed")
		return
	}
	writeTokenResponse(w, status, pair, userID, username)
}

// POST /auth/register  { "username", "password" }
func handleRegister(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, "Invalid JSON: "+err.Error())
		return
	}
	username := normalizeUsername(body.Username)
	if username == "" || body.Password == "" {
		writeError(w, 400, "username and password are required")
		return
	}
	if !validateUsername(username) {
		writeError(w, 400, "username must be 3–32 chars: lowercase letters, digits, . _ -")
		return
	}
	if !validatePassword(body.Password) {
		writeError(w, 400, "password must be 8–72 characters")
		return
	}

	ctx, cancel := withTimeout(r)
	defer cancel()

	var existing string
	err := db.QueryRow(ctx, `SELECT id FROM users WHERE lower(username) = $1`, username).Scan(&existing)
	if err == nil {
		writeError(w, 409, "Username already taken")
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		log.Printf("user-server register lookup: %v", err)
		writeError(w, 500, "Database error")
		return
	}

	passwordHash, err := hashPassword(body.Password)
	if err != nil {
		log.Printf("user-server register hash: %v", err)
		writeError(w, 500, "Internal error")
		return
	}

	// Username MVP: no email yet — leave verification false until a real flow exists.
	var userID string
	err = db.QueryRow(ctx, `
		INSERT INTO users (username, email, password_hash, email_verified)
		VALUES ($1, NULL, $2, false)
		RETURNING id
	`, username, passwordHash).Scan(&userID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			writeError(w, 409, "Username already taken")
			return
		}
		log.Printf("user-server register insert: %v", err)
		writeError(w, 500, "Database error")
		return
	}

	log.Printf("user-server register: %s", username)
	issueAndStoreSession(w, r, 201, userID, username)
}

// POST /auth/login  { "username", "password" }
func handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, "Invalid JSON: "+err.Error())
		return
	}
	username := normalizeUsername(body.Username)
	if username == "" || body.Password == "" {
		writeError(w, 400, "username and password are required")
		return
	}

	ctx, cancel := withTimeout(r)
	defer cancel()

	var userID, passwordHash string
	err := db.QueryRow(ctx, `
		SELECT id, password_hash FROM users WHERE lower(username) = $1
	`, username).Scan(&userID, &passwordHash)
	if err != nil {
		writeError(w, 401, "Invalid username or password")
		return
	}
	if !checkPassword(passwordHash, body.Password) {
		writeError(w, 401, "Invalid username or password")
		return
	}

	log.Printf("user-server login: %s", username)
	issueAndStoreSession(w, r, 200, userID, username)
}

// POST /auth/refresh  { "refresh_token" }
func handleRefresh(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, "Invalid JSON: "+err.Error())
		return
	}
	if body.RefreshToken == "" {
		writeError(w, 400, "refresh_token is required")
		return
	}

	claims, err := parseRefreshClaims(body.RefreshToken)
	if err != nil {
		writeError(w, 401, "Invalid or expired refresh token")
		return
	}

	ctx, cancel := withTimeout(r)
	defer cancel()

	var username string
	err = db.QueryRow(ctx, `SELECT username FROM users WHERE id = $1`, claims.UserID).Scan(&username)
	if err != nil {
		writeError(w, 401, "User not found")
		return
	}
	username = normalizeUsername(username)

	pair, err := generateTokenPair(claims.UserID, username)
	if err != nil {
		log.Printf("user-server refresh token: %v", err)
		writeError(w, 500, "Token generation failed")
		return
	}

	_, err = rotateSession(ctx, claims.UserID, body.RefreshToken, pair.RefreshToken, requestUserAgent(r), refreshExpiresAt())
	if err != nil {
		switch {
		case errors.Is(err, errSessionReuse):
			log.Printf("user-server refresh reuse: user=%s", claims.UserID)
			writeError(w, 401, "Refresh token reuse detected; all sessions revoked")
		case errors.Is(err, errSessionNotFound), errors.Is(err, errSessionRevoked), errors.Is(err, errSessionExpired):
			writeError(w, 401, "Invalid or expired refresh token")
		default:
			log.Printf("user-server refresh rotate: %v", err)
			writeError(w, 500, "Session rotation failed")
		}
		return
	}

	writeTokenResponse(w, 200, pair, claims.UserID, username)
}

// POST /auth/logout  { "refresh_token" } — revoke this refresh session only.
func handleLogout(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, "Invalid JSON: "+err.Error())
		return
	}
	if body.RefreshToken == "" {
		writeError(w, 400, "refresh_token is required")
		return
	}

	ctx, cancel := withTimeout(r)
	defer cancel()

	if err := revokeSessionByHash(ctx, body.RefreshToken); err != nil {
		if errors.Is(err, errSessionNotFound) {
			// Idempotent: already gone / never stored.
			writeJSON(w, 200, map[string]bool{"revoked": true})
			return
		}
		log.Printf("user-server logout: %v", err)
		writeError(w, 500, "Logout failed")
		return
	}
	writeJSON(w, 200, map[string]bool{"revoked": true})
}

// POST /auth/change-password  { "username", "current_password", "new_password" }
// Updates the password hash and revokes all refresh sessions for the user.
func handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username        string `json:"username"`
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, "Invalid JSON: "+err.Error())
		return
	}
	username := normalizeUsername(body.Username)
	if username == "" || body.CurrentPassword == "" || body.NewPassword == "" {
		writeError(w, 400, "username, current_password, and new_password are required")
		return
	}
	if !validatePassword(body.NewPassword) {
		writeError(w, 400, "password must be 8–72 characters")
		return
	}

	ctx, cancel := withTimeout(r)
	defer cancel()

	var userID, passwordHash string
	err := db.QueryRow(ctx, `
		SELECT id, password_hash FROM users WHERE lower(username) = $1
	`, username).Scan(&userID, &passwordHash)
	if err != nil {
		writeError(w, 401, "Invalid username or password")
		return
	}
	if !checkPassword(passwordHash, body.CurrentPassword) {
		writeError(w, 401, "Invalid username or password")
		return
	}

	newHash, err := hashPassword(body.NewPassword)
	if err != nil {
		log.Printf("user-server change-password hash: %v", err)
		writeError(w, 500, "Internal error")
		return
	}

	n, err := updatePasswordAndRevokeSessions(ctx, userID, newHash)
	if err != nil {
		log.Printf("user-server change-password transaction: %v", err)
		writeError(w, 500, "Password update failed")
		return
	}

	log.Printf("user-server change-password: %s revoked_sessions=%d", username, n)
	writeJSON(w, 200, map[string]interface{}{
		"ok":               true,
		"sessions_revoked": n,
	})
}
