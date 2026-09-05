package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestResolveJWTSecret(t *testing.T) {
	tests := []struct {
		name  string
		raw   string
		allow bool
		want  string
		ok    bool
	}{
		{"strong", "a-real-random-secret", false, "a-real-random-secret", true},
		{"empty production", "", false, "", false},
		{"empty dev", "", true, "dev-jwt-secret-change-in-production", true},
		{"default denied in dev", "dev-jwt-secret-change-in-production", true, "", false},
		{"placeholder denied", "change-me-in-production", false, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveJWTSecret(tt.raw, tt.allow)
			if (err == nil) != tt.ok {
				t.Fatalf("resolveJWTSecret(%q, %t) err=%v", tt.raw, tt.allow, err)
			}
			if string(got) != tt.want {
				t.Fatalf("secret=%q want %q", got, tt.want)
			}
		})
	}
}

func TestExtractBearerToken(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   string
	}{
		{"empty header", "", ""},
		{"no bearer prefix", "sometoken", ""},
		{"bearer with token", "Bearer abc.def.ghi", "abc.def.ghi"},
		{"bearer with empty token (exactly 'Bearer ')", "Bearer ", ""},
		{"lowercase bearer not recognized", "bearer abc", ""},
		{"bearer prefix but no space handling wrong case", "Bearerabc", ""},
		{"basic auth scheme ignored", "Basic dXNlcjpwYXNz", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			if got := extractBearerToken(req); got != tt.want {
				t.Fatalf("extractBearerToken(%q) = %q, want %q", tt.header, got, tt.want)
			}
		})
	}
}

func TestGenerateAndParseAccessTokenRoundTrip(t *testing.T) {
	tok, expiresAt, err := generateAccessToken("user-1", "alice")
	if err != nil {
		t.Fatalf("generateAccessToken: %v", err)
	}
	if tok == "" {
		t.Fatal("expected non-empty token")
	}
	if expiresAt.Before(time.Now()) {
		t.Fatal("expiresAt should be in the future")
	}

	claims, err := parseAccessClaims(tok)
	if err != nil {
		t.Fatalf("parseAccessClaims: %v", err)
	}
	if claims.UserID != "user-1" || claims.Username != "alice" {
		t.Fatalf("claims mismatch: %+v", claims)
	}
	if claims.Typ != typAccess {
		t.Fatalf("expected typ=%q, got %q", typAccess, claims.Typ)
	}
}

func TestParseAccessClaimsMalformedToken(t *testing.T) {
	if _, err := parseAccessClaims("not-a-jwt"); err == nil {
		t.Fatal("expected error for malformed token")
	}
	if _, err := parseAccessClaims(""); err == nil {
		t.Fatal("expected error for empty token")
	}
}

func TestParseAccessClaimsExpiredToken(t *testing.T) {
	claims := authClaims{
		UserID: "user-1",
		Typ:    typAccess,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "user-1",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-1 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-2 * time.Hour)),
			Issuer:    issuer,
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(jwtSecret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseAccessClaims(signed); err == nil {
		t.Fatal("expected error for expired token")
	}
}

func TestParseAccessClaimsRejectsRefreshToken(t *testing.T) {
	claims := authClaims{
		UserID: "user-1",
		Typ:    typRefresh,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "user-1",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    issuer,
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(jwtSecret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseAccessClaims(signed); err == nil {
		t.Fatal("expected refresh token to be rejected for API access")
	}
}

func TestParseAccessClaimsRejectsWrongSigningMethod(t *testing.T) {
	claims := authClaims{
		UserID: "user-1",
		Typ:    typAccess,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "user-1",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    issuer,
		},
	}
	// alg=none is a classic JWT vuln vector; ensure it's rejected.
	token := jwt.NewWithClaims(jwt.SigningMethodNone, claims)
	signed, err := token.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseAccessClaims(signed); err == nil {
		t.Fatal("expected alg=none token to be rejected")
	}
}

func TestParseAccessClaimsRejectsWrongSecret(t *testing.T) {
	claims := authClaims{
		UserID: "user-1",
		Typ:    typAccess,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "user-1",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    issuer,
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte("some-other-secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseAccessClaims(signed); err == nil {
		t.Fatal("expected token signed with wrong secret to be rejected")
	}
}

func TestParseAccessClaimsFallsBackToSubjectWhenUserIDMissing(t *testing.T) {
	claims := authClaims{
		Typ: typAccess,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "legacy-subject",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    issuer,
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(jwtSecret)
	if err != nil {
		t.Fatal(err)
	}
	got, err := parseAccessClaims(signed)
	if err != nil {
		t.Fatalf("parseAccessClaims: %v", err)
	}
	if got.UserID != "legacy-subject" {
		t.Fatalf("expected UserID to fall back to Subject, got %q", got.UserID)
	}
}

func TestParseAccessClaimsRejectsMissingSubjectAndUserID(t *testing.T) {
	claims := authClaims{
		Typ: typAccess,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    issuer,
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(jwtSecret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseAccessClaims(signed); err == nil {
		t.Fatal("expected error when both UserID and Subject are missing")
	}
}

func TestClaimsFromContextNilWhenAbsent(t *testing.T) {
	if got := claimsFromContext(context.Background()); got != nil {
		t.Fatalf("expected nil claims for bare context, got %+v", got)
	}
}

func TestClaimsFromContextReturnsStoredClaims(t *testing.T) {
	want := &authClaims{UserID: "u1"}
	ctx := context.WithValue(context.Background(), userClaimsKey, want)
	got := claimsFromContext(ctx)
	if got != want {
		t.Fatalf("claimsFromContext = %+v, want %+v", got, want)
	}
}

func TestMustClaimsMissingWrites401(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	claims, ok := mustClaims(rec, req, "auth required")
	if ok || claims != nil {
		t.Fatalf("expected ok=false, claims=nil; got ok=%v claims=%+v", ok, claims)
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestMustClaimsPresent(t *testing.T) {
	want := &authClaims{UserID: "u1"}
	ctx := context.WithValue(context.Background(), userClaimsKey, want)
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	claims, ok := mustClaims(rec, req, "auth required")
	if !ok || claims != want {
		t.Fatalf("expected ok=true claims=%+v, got ok=%v claims=%+v", want, ok, claims)
	}
}

func TestRequireJWTMiddleware(t *testing.T) {
	tok, _, err := generateAccessToken("user-mw", "mwuser")
	if err != nil {
		t.Fatal(err)
	}

	var sawClaims *authClaims
	handler := requireJWT(func(w http.ResponseWriter, r *http.Request) {
		sawClaims = claimsFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	t.Run("missing header", func(t *testing.T) {
		sawClaims = nil
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		handler(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", rec.Code)
		}
		if sawClaims != nil {
			t.Fatal("handler must not run without a token")
		}
	})

	t.Run("invalid token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer garbage")
		rec := httptest.NewRecorder()
		handler(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", rec.Code)
		}
	})

	t.Run("valid token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		handler(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		if sawClaims == nil || sawClaims.UserID != "user-mw" {
			t.Fatalf("expected claims to be injected into context, got %+v", sawClaims)
		}
	})
}

func TestAllowInsecureJWTSecret(t *testing.T) {
	t.Setenv("DEV_MODE", "1")
	if !allowInsecureJWTSecret() {
		t.Fatal("DEV_MODE=1 should allow insecure secret")
	}
	t.Setenv("DEV_MODE", "")
	// Running under `go test`, os.Args[0] ends in ".test" or carries -test.
	// flags, so allowInsecureJWTSecret() should still return true here.
	if !allowInsecureJWTSecret() {
		t.Fatal("expected go test binary args to be detected as insecure-allowed")
	}
}
