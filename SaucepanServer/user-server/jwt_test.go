package main

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestResolveJWTSecret(t *testing.T) {
	tests := []struct {
		raw   string
		allow bool
		ok    bool
	}{
		{strings.Repeat("r", minJWTSecretBytes), false, true},
		{"", false, false},
		{"", true, true},
		{"short-secret", false, false},
		{"dev-jwt-secret-change-in-production", true, false},
		{"change-me-in-production", false, false},
		{"local-dev-only-not-for-vps", true, false},
		{"local-dev-only-not-for-vps", false, false},
		{"change-me-in-production", true, false},
	}
	for _, tt := range tests {
		got, err := resolveJWTSecret(tt.raw, tt.allow)
		if (err == nil) != tt.ok {
			t.Fatalf("resolveJWTSecret(%q, %t) err=%v", tt.raw, tt.allow, err)
		}
		if tt.ok && len(got) == 0 {
			t.Fatalf("resolveJWTSecret(%q, %t) returned empty secret on success", tt.raw, tt.allow)
		}
	}
}

func TestResolveJWTSecretEmptyUsesEphemeralSecret(t *testing.T) {
	a, err := resolveJWTSecret("", true)
	if err != nil {
		t.Fatal(err)
	}
	b, err := resolveJWTSecret("", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != minJWTSecretBytes || len(b) != minJWTSecretBytes {
		t.Fatalf("ephemeral secret lengths = %d, %d; want %d", len(a), len(b), minJWTSecretBytes)
	}
	if string(a) == string(b) {
		t.Fatal("ephemeral development secrets must not be fixed or reused")
	}
}

func TestResolveJWTSecret_EmptyDisallowedReturnsNilSecret(t *testing.T) {
	secret, err := resolveJWTSecret("", false)
	if err == nil {
		t.Fatal("expected error for empty secret with allowInsecure=false")
	}
	if secret != nil {
		t.Fatalf("expected nil secret on error, got %v", secret)
	}
}

func TestNewTokenID(t *testing.T) {
	a, err := newTokenID()
	if err != nil {
		t.Fatal(err)
	}
	b, err := newTokenID()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("expected unique token ids")
	}
	if len(a) != 32 { // 16 bytes hex-encoded
		t.Fatalf("expected 32 hex chars, got %d (%s)", len(a), a)
	}
}

func TestAllowInsecureJWTSecret(t *testing.T) {
	// Running under `go test`, os.Args[0] ends in ".test" (or contains -test.
	// flags), so this should be true in this test binary regardless of DEV_MODE.
	if !allowInsecureJWTSecret() {
		t.Fatal("expected allowInsecureJWTSecret to be true under go test")
	}
}

func TestGenerateAccessToken(t *testing.T) {
	before := time.Now()
	tok, expiresAt, err := generateAccessToken("user-123", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if tok == "" {
		t.Fatal("expected non-empty token")
	}
	if !expiresAt.After(before) {
		t.Fatal("expected expiry in the future")
	}
	if diff := expiresAt.Sub(before); diff < accessTokenTTL-time.Second || diff > accessTokenTTL+time.Second {
		t.Fatalf("expected ~%v TTL, got %v", accessTokenTTL, diff)
	}

	parsed, err := jwt.ParseWithClaims(tok, &accessClaims{}, func(t *jwt.Token) (interface{}, error) {
		return jwtSecret, nil
	})
	if err != nil || !parsed.Valid {
		t.Fatalf("expected parseable/valid token, err=%v", err)
	}
	claims := parsed.Claims.(*accessClaims)
	if claims.UserID != "user-123" || claims.Username != "alice" || claims.Typ != typAccess {
		t.Fatalf("unexpected claims: %+v", claims)
	}
	if claims.Issuer != issuer {
		t.Fatalf("expected issuer %q, got %q", issuer, claims.Issuer)
	}
}

func TestGenerateRefreshToken(t *testing.T) {
	tok, err := generateRefreshToken("user-456", "bob")
	if err != nil {
		t.Fatal(err)
	}
	if tok == "" {
		t.Fatal("expected non-empty token")
	}
	claims, err := parseRefreshClaims(tok)
	if err != nil {
		t.Fatalf("expected valid refresh token, err=%v", err)
	}
	if claims.UserID != "user-456" || claims.Username != "bob" || claims.Typ != typRefresh {
		t.Fatalf("unexpected claims: %+v", claims)
	}
	if claims.ID == "" {
		t.Fatal("expected non-empty jti")
	}
}

func TestGenerateTokenPair(t *testing.T) {
	pair, err := generateTokenPair("user-789", "carol")
	if err != nil {
		t.Fatal(err)
	}
	if pair.AccessToken == "" || pair.RefreshToken == "" {
		t.Fatal("expected both tokens set")
	}
	if pair.AccessToken == pair.RefreshToken {
		t.Fatal("access and refresh tokens must differ")
	}
	if pair.ExpiresAt.IsZero() {
		t.Fatal("expected non-zero ExpiresAt")
	}
}

func TestParseRefreshClaims(t *testing.T) {
	t.Run("valid round trip", func(t *testing.T) {
		tok, err := generateRefreshToken("uid-1", "dave")
		if err != nil {
			t.Fatal(err)
		}
		claims, err := parseRefreshClaims(tok)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if claims.UserID != "uid-1" {
			t.Fatalf("unexpected user id: %s", claims.UserID)
		}
	})

	t.Run("garbage string", func(t *testing.T) {
		if _, err := parseRefreshClaims("not-a-jwt-at-all"); err == nil {
			t.Fatal("expected error for garbage token")
		}
	})

	t.Run("empty string", func(t *testing.T) {
		if _, err := parseRefreshClaims(""); err == nil {
			t.Fatal("expected error for empty token")
		}
	})

	t.Run("access token rejected as refresh", func(t *testing.T) {
		access, _, err := generateAccessToken("uid-2", "eve")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parseRefreshClaims(access); err == nil {
			t.Fatal("expected access token to be rejected as refresh token")
		}
	})

	t.Run("expired token rejected", func(t *testing.T) {
		claims := refreshClaims{
			UserID:   "uid-3",
			Username: "frank",
			Typ:      typRefresh,
			RegisteredClaims: jwt.RegisteredClaims{
				ID:        "jti-expired",
				Subject:   "uid-3",
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Hour)),
				IssuedAt:  jwt.NewNumericDate(time.Now().Add(-2 * time.Hour)),
				Issuer:    issuer,
			},
		}
		tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		signed, err := tok.SignedString(jwtSecret)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parseRefreshClaims(signed); err == nil {
			t.Fatal("expected error for expired token")
		}
	})

	t.Run("wrong signing method rejected", func(t *testing.T) {
		claims := refreshClaims{
			UserID: "uid-4",
			Typ:    typRefresh,
			RegisteredClaims: jwt.RegisteredClaims{
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			},
		}
		tok := jwt.NewWithClaims(jwt.SigningMethodNone, claims)
		signed, err := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parseRefreshClaims(signed); err == nil {
			t.Fatal("expected error for none-alg token")
		}
	})

	t.Run("wrong secret rejected", func(t *testing.T) {
		claims := refreshClaims{
			UserID: "uid-5",
			Typ:    typRefresh,
			RegisteredClaims: jwt.RegisteredClaims{
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			},
		}
		tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		signed, err := tok.SignedString([]byte("some-other-secret-entirely"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parseRefreshClaims(signed); err == nil {
			t.Fatal("expected error for token signed with wrong secret")
		}
	})

	t.Run("missing typ rejected", func(t *testing.T) {
		claims := refreshClaims{
			UserID: "uid-6",
			Typ:    "",
			RegisteredClaims: jwt.RegisteredClaims{
				Subject:   "uid-6",
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			},
		}
		tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		signed, err := tok.SignedString(jwtSecret)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parseRefreshClaims(signed); err == nil {
			t.Fatal("expected error for missing typ claim")
		}
	})
}

func TestWriteTokenResponse(t *testing.T) {
	rec := httptest.NewRecorder()
	pair := &tokenPair{
		AccessToken:  "access-abc",
		RefreshToken: "refresh-xyz",
		ExpiresAt:    time.Now().Add(time.Hour),
	}
	writeTokenResponse(rec, 200, pair, "uid-1", "grace")
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"access-abc", "refresh-xyz", "uid-1", "grace"} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected body to contain %q, got %s", want, body)
		}
	}
}
