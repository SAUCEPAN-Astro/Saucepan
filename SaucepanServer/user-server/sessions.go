package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	errSessionNotFound = errors.New("session not found")
	errSessionRevoked  = errors.New("session revoked")
	errSessionExpired  = errors.New("session expired")
	errSessionReuse    = errors.New("refresh token reuse detected")
)

func hashRefreshToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func createSession(ctx context.Context, userID, refreshToken, userAgent string, expiresAt time.Time) (string, error) {
	var id string
	err := db.QueryRow(ctx, `
		INSERT INTO user_sessions (user_id, token_hash, user_agent, expires_at)
		VALUES ($1, $2, NULLIF($3, ''), $4)
		RETURNING id
	`, userID, hashRefreshToken(refreshToken), userAgent, expiresAt).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("insert user_session: %w", err)
	}
	return id, nil
}

// rotateSession validates the presented refresh hash, marks the old row revoked,
// and inserts a new session. Reuse of a revoked token revokes all sessions for the user.
func rotateSession(ctx context.Context, userID, oldRefresh, newRefresh, userAgent string, expiresAt time.Time) (string, error) {
	oldHash := hashRefreshToken(oldRefresh)
	tx, err := db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)

	var (
		sessionID  string
		ownerID    string
		revokedAt  *time.Time
		replacedBy *string
		exp        time.Time
	)
	err = tx.QueryRow(ctx, `
		SELECT id, user_id, revoked_at, replaced_by, expires_at
		FROM user_sessions
		WHERE token_hash = $1
		FOR UPDATE
	`, oldHash).Scan(&sessionID, &ownerID, &revokedAt, &replacedBy, &exp)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errSessionNotFound
	}
	if err != nil {
		return "", err
	}
	if ownerID != userID {
		return "", errSessionNotFound
	}
	if revokedAt != nil {
		// Reuse detection: only when this token was superseded by rotation
		// (someone still holds a previous refresh). Plain logout/password
		// revoke does not cascade to other devices.
		if replacedBy != nil {
			if _, err := tx.Exec(ctx, `
				UPDATE user_sessions
				SET revoked_at = COALESCE(revoked_at, NOW())
				WHERE user_id = $1 AND revoked_at IS NULL
			`, userID); err != nil {
				return "", fmt.Errorf("revoke sessions after refresh reuse: %w", err)
			}
			if err := tx.Commit(ctx); err != nil {
				return "", fmt.Errorf("commit refresh reuse revocation: %w", err)
			}
			return "", errSessionReuse
		}
		return "", errSessionRevoked
	}
	if time.Now().After(exp) {
		return "", errSessionExpired
	}

	var newID string
	err = tx.QueryRow(ctx, `
		INSERT INTO user_sessions (user_id, token_hash, user_agent, expires_at)
		VALUES ($1, $2, NULLIF($3, ''), $4)
		RETURNING id
	`, userID, hashRefreshToken(newRefresh), userAgent, expiresAt).Scan(&newID)
	if err != nil {
		return "", fmt.Errorf("insert rotated session: %w", err)
	}

	_, err = tx.Exec(ctx, `
		UPDATE user_sessions
		SET revoked_at = NOW(), replaced_by = $2
		WHERE id = $1 AND revoked_at IS NULL
	`, sessionID, newID)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return newID, nil
}

// updatePasswordAndRevokeSessions changes the password and invalidates every
// refresh session in one transaction, so a partial password reset is never
// committed.
func updatePasswordAndRevokeSessions(ctx context.Context, userID, passwordHash string) (int64, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		UPDATE users SET password_hash = $2, updated_at = NOW() WHERE id = $1
	`, userID, passwordHash); err != nil {
		return 0, err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE user_sessions
		SET revoked_at = NOW()
		WHERE user_id = $1 AND revoked_at IS NULL
	`, userID)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func revokeSessionByHash(ctx context.Context, refreshToken string) error {
	tag, err := db.Exec(ctx, `
		UPDATE user_sessions
		SET revoked_at = NOW()
		WHERE token_hash = $1 AND revoked_at IS NULL
	`, hashRefreshToken(refreshToken))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errSessionNotFound
	}
	return nil
}

func revokeAllUserSessions(ctx context.Context, userID string) (int64, error) {
	tag, err := db.Exec(ctx, `
		UPDATE user_sessions
		SET revoked_at = NOW()
		WHERE user_id = $1 AND revoked_at IS NULL
	`, userID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func refreshExpiresAt() time.Time {
	return time.Now().Add(refreshTokenTTL)
}
