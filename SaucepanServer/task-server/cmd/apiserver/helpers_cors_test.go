package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCorsOriginsAllowlistFailClosed(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "")
	t.Setenv("DEV_MODE", "")
	if got := corsOriginsAllowlist(); len(got) != 0 {
		t.Fatalf("prod default allowlist=%v want empty", got)
	}
}

func TestCorsOriginsAllowlistDevDefaults(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "")
	t.Setenv("DEV_MODE", "1")
	got := corsOriginsAllowlist()
	for _, want := range corsDevDefaultOrigins {
		if _, ok := got[want]; !ok {
			t.Fatalf("DEV_MODE default missing %q in %v", want, got)
		}
	}
}

func TestCorsOriginsAllowlistExplicitIgnoresStar(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "https://app.example.com, * ,https://dash.example.com")
	t.Setenv("DEV_MODE", "")
	got := corsOriginsAllowlist()
	if _, ok := got["*"]; ok {
		t.Fatal("wildcard * must never be allowlisted")
	}
	if _, ok := got["https://app.example.com"]; !ok {
		t.Fatal("expected app.example.com")
	}
	if _, ok := got["https://dash.example.com"]; !ok {
		t.Fatal("expected dash.example.com")
	}
}

func TestCorsMiddlewareDisallowedOriginOmitsACAO(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "https://trusted.example")
	t.Setenv("DEV_MODE", "")

	h := corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Authorization", "Bearer fake")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if acao := rec.Header().Get("Access-Control-Allow-Origin"); acao != "" {
		t.Fatalf("disallowed Origin got ACAO=%q want omitted", acao)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (request still served; browser blocks read)", rec.Code)
	}
}

func TestCorsMiddlewareAllowedOriginReflects(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "https://trusted.example")
	t.Setenv("DEV_MODE", "")

	h := corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.Header.Set("Origin", "https://trusted.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://trusted.example" {
		t.Fatalf("ACAO=%q want https://trusted.example", got)
	}
	if got := rec.Header().Get("Vary"); got != "Origin" {
		t.Fatalf("Vary=%q want Origin", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got != corsAllowHeaders {
		t.Fatalf("Allow-Headers=%q", got)
	}
}

func TestCorsMiddlewarePreflightDisallowedOmitsACAO(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "https://trusted.example")
	t.Setenv("DEV_MODE", "")

	h := corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not run on OPTIONS")
	}))

	req := httptest.NewRequest(http.MethodOptions, "/api/v1/campaigns", nil)
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Access-Control-Request-Method", "GET")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d want 204", rec.Code)
	}
	if acao := rec.Header().Get("Access-Control-Allow-Origin"); acao != "" {
		t.Fatalf("preflight disallowed Origin got ACAO=%q", acao)
	}
}

func TestCorsMiddlewareNoOriginOmitsACAO(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "https://trusted.example")
	t.Setenv("DEV_MODE", "")

	h := corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if acao := rec.Header().Get("Access-Control-Allow-Origin"); acao != "" {
		t.Fatalf("no Origin got ACAO=%q", acao)
	}
}
