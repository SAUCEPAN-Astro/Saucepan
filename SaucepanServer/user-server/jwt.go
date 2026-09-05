package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func newTokenID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

var blockedJWTSecrets = map[string]struct{}{
	"":                                    {},
	"dev-jwt-secret-change-in-production": {},
	"change-me-in-production":             {},
	"local-dev-only-not-for-vps":          {},
}

func allowInsecureJWTSecret() bool {
	if os.Getenv("DEV_MODE") == "1" {
		return true
	}
	for _, arg := range os.Args {
		if strings.HasPrefix(arg, "-test.") || strings.HasSuffix(arg, ".test") {
			return true
		}
	}
	return false
}

func resolveJWTSecret(raw string, allowInsecure bool) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		if allowInsecure {
			var secret [minJWTSecretBytes]byte
			if _, err := rand.Read(secret[:]); err != nil {
				return nil, fmt.Errorf("generate ephemeral JWT_SECRET: %w", err)
			}
			log.Println("WARNING: empty JWT_SECRET allowed only for local development/tests; using an ephemeral secret")
			return secret[:], nil
		}
		return nil, errors.New("JWT_SECRET must be set to a non-default value")
	}
	if _, blocked := blockedJWTSecrets[raw]; blocked {
		return nil, errors.New("JWT_SECRET matches a known-weak placeholder; generate a new secret")
	}
	if len([]byte(raw)) < minJWTSecretBytes {
		return nil, fmt.Errorf("JWT_SECRET must be at least %d bytes", minJWTSecretBytes)
	}
	return []byte(raw), nil
}

var jwtSecret = func() []byte {
	secret, err := resolveJWTSecret(os.Getenv("JWT_SECRET"), allowInsecureJWTSecret())
	if err != nil {
		log.Fatal(err)
	}
	return secret
}()

const (
	accessTokenTTL    = 24 * time.Hour
	refreshTokenTTL   = 30 * 24 * time.Hour
	minJWTSecretBytes = 32
	typAccess         = "access"
	typRefresh        = "refresh"
	issuer            = "saucepan-api"
)

// accessClaims — shared contract with task-server apiserver requireJWT.
// sub = user UUID; username = login id; typ = "access".
type accessClaims struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	Typ      string `json:"typ"`
	jwt.RegisteredClaims
}

type refreshClaims struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	Typ      string `json:"typ"`
	jwt.RegisteredClaims
}

type tokenPair struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

func generateAccessToken(userID, username string) (string, time.Time, error) {
	expiresAt := time.Now().Add(accessTokenTTL)
	claims := accessClaims{
		UserID:   userID,
		Username: username,
		Typ:      typAccess,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    issuer,
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(jwtSecret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign access token: %w", err)
	}
	return signed, expiresAt, nil
}

func generateRefreshToken(userID, username string) (string, error) {
	jti, err := newTokenID()
	if err != nil {
		return "", fmt.Errorf("refresh jti: %w", err)
	}
	expiresAt := time.Now().Add(refreshTokenTTL)
	claims := refreshClaims{
		UserID:   userID,
		Username: username,
		Typ:      typRefresh,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        jti,
			Subject:   userID,
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    issuer,
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(jwtSecret)
}

func generateTokenPair(userID, username string) (*tokenPair, error) {
	access, expiresAt, err := generateAccessToken(userID, username)
	if err != nil {
		return nil, err
	}
	refresh, err := generateRefreshToken(userID, username)
	if err != nil {
		return nil, err
	}
	return &tokenPair{
		AccessToken:  access,
		RefreshToken: refresh,
		ExpiresAt:    expiresAt,
	}, nil
}

func parseRefreshClaims(tokenStr string) (*refreshClaims, error) {
	claims := &refreshClaims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return jwtSecret, nil
	})
	if err != nil || !token.Valid {
		return nil, errors.New("invalid or expired refresh token")
	}
	if claims.Typ != typRefresh && claims.Typ != "" {
		// Accept legacy token_type via MapClaims fallback below if needed.
		return nil, errors.New("not a refresh token")
	}
	if claims.Typ == "" {
		// Legacy refresh used token_type=refresh; reject unless typ is set.
		return nil, errors.New("not a refresh token")
	}
	if claims.UserID == "" && claims.Subject != "" {
		claims.UserID = claims.Subject
	}
	return claims, nil
}

func writeTokenResponse(w http.ResponseWriter, status int, pair *tokenPair, userID, username string) {
	writeJSON(w, status, map[string]interface{}{
		"access_token":  pair.AccessToken,
		"refresh_token": pair.RefreshToken,
		"expires_at":    pair.ExpiresAt.Format(time.RFC3339),
		"user": map[string]string{
			"id":       userID,
			"username": username,
		},
	})
}
