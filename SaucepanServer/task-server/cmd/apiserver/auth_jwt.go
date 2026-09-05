package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

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
	if raw == "" {
		if allowInsecure {
			log.Println("WARNING: empty JWT_SECRET allowed only because DEV_MODE=1 or go test")
			return []byte("dev-jwt-secret-change-in-production"), nil
		}
		return nil, errors.New("JWT_SECRET must be set to a non-default value")
	}
	if _, blocked := blockedJWTSecrets[raw]; blocked {
		return nil, errors.New("JWT_SECRET matches a known-weak placeholder; generate a new secret")
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
	accessTokenTTL = 24 * time.Hour
	typAccess      = "access"
	typRefresh     = "refresh"
	issuer         = "saucepan-api"
)

// authClaims match user-server access JWTs (shared JWT_SECRET).
// Legacy tokens may omit typ / username and carry email instead.
type authClaims struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	Typ      string `json:"typ"`
	Email    string `json:"email"` // legacy
	jwt.RegisteredClaims
}

func generateAccessToken(userID, username string) (string, time.Time, error) {
	expiresAt := time.Now().Add(accessTokenTTL)
	claims := authClaims{
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

func parseAccessClaims(tokenStr string) (*authClaims, error) {
	claims := &authClaims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return jwtSecret, nil
	})
	if err != nil || !token.Valid {
		return nil, errors.New("invalid or expired token")
	}
	if claims.Typ == typRefresh {
		return nil, errors.New("refresh token not valid for API access")
	}
	if claims.UserID == "" && claims.Subject != "" {
		claims.UserID = claims.Subject
	}
	if claims.UserID == "" {
		return nil, errors.New("token missing subject")
	}
	return claims, nil
}

type contextKey string

const userClaimsKey contextKey = "userClaims"

func claimsFromContext(ctx context.Context) *authClaims {
	c, _ := ctx.Value(userClaimsKey).(*authClaims)
	return c
}

// mustClaims extracts JWT claims from the request context. If absent, it
// writes a 401 response with msg and returns ok=false; callers should return
// immediately in that case. It does not check claims.UserID — sites that
// also require a non-empty UserID must keep their own check.
func mustClaims(w http.ResponseWriter, r *http.Request, msg string) (*authClaims, bool) {
	claims := claimsFromContext(r.Context())
	if claims == nil {
		writeError(w, 401, msg)
		return nil, false
	}
	return claims, true
}

func requireJWT(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tokenStr := extractBearerToken(r)
		if tokenStr == "" {
			writeError(w, 401, "Missing Authorization header")
			return
		}
		claims, err := parseAccessClaims(tokenStr)
		if err != nil {
			writeError(w, 401, "Invalid or expired token")
			return
		}
		ctx := context.WithValue(r.Context(), userClaimsKey, claims)
		next(w, r.WithContext(ctx))
	}
}

func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if len(auth) > 7 && auth[:7] == "Bearer " {
		return auth[7:]
	}
	return ""
}
