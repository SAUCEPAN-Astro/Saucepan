package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func setupAuthRateLimitTest(t *testing.T, handler http.HandlerFunc) (*httptest.Server, func()) {
	t.Helper()
	backend := &memoryRateBackend{}
	authLimiter = &authRateLimiter{backend: backend}

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on IPv4 loopback: %v", err)
	}
	mock := &httptest.Server{Listener: listener, Config: &http.Server{Handler: handler}}
	mock.Start()
	oldURL := userServiceURL
	userServiceURL = mock.URL

	return mock, func() {
		mock.Close()
		userServiceURL = oldURL
	}
}

func authRequest(method, path string, body []byte, remoteAddr string) (*http.Request, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = remoteAddr
	rec := httptest.NewRecorder()
	return req, rec
}

func TestAuthRegisterRateLimitPerIP(t *testing.T) {
	mock, cleanup := setupAuthRateLimitTest(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"access_token":"t"}`))
	})
	defer cleanup()

	body := []byte(`{"username":"alice","password":"password123"}`)
	for i := 0; i < authRegisterLimit; i++ {
		req, rec := authRequest(http.MethodPost, "/auth/register", body, "203.0.113.10:12345")
		handleAuthRegister(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("request %d: status=%d want 201", i+1, rec.Code)
		}
	}

	req, rec := authRequest(http.MethodPost, "/auth/register", body, "203.0.113.10:12345")
	handleAuthRegister(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("missing Retry-After header")
	}
	if secs, err := strconv.Atoi(rec.Header().Get("Retry-After")); err != nil || secs < 1 {
		t.Fatalf("Retry-After invalid: %q", rec.Header().Get("Retry-After"))
	}

	req2, rec2 := authRequest(http.MethodPost, "/auth/register", body, "203.0.113.11:12345")
	handleAuthRegister(rec2, req2)
	if rec2.Code != http.StatusCreated {
		t.Fatalf("other IP status=%d want 201", rec2.Code)
	}

	_ = mock
}

func TestAuthLoginFailedRateLimitPerIPAndUsername(t *testing.T) {
	mock, cleanup := setupAuthRateLimitTest(t, func(w http.ResponseWriter, r *http.Request) {
		writeError(w, 401, "Invalid username or password")
	})
	defer cleanup()

	body := []byte(`{"username":"bob","password":"wrong"}`)
	for i := 0; i < authLoginFailLimit; i++ {
		req, rec := authRequest(http.MethodPost, "/auth/login", body, "198.51.100.5:12345")
		handleAuthLogin(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("request %d: status=%d want 401", i+1, rec.Code)
		}
	}

	req, rec := authRequest(http.MethodPost, "/auth/login", body, "198.51.100.5:12345")
	handleAuthLogin(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("missing Retry-After header")
	}

	otherUser := []byte(`{"username":"carol","password":"wrong"}`)
	req2, rec2 := authRequest(http.MethodPost, "/auth/login", otherUser, "198.51.100.5:12345")
	handleAuthLogin(rec2, req2)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("other username status=%d want 401", rec2.Code)
	}

	_ = mock
}

func TestAuthLoginSuccessDoesNotCountTowardLimit(t *testing.T) {
	_, cleanup := setupAuthRateLimitTest(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"access_token":"ok"}`))
	})
	defer cleanup()

	body := []byte(`{"username":"dave","password":"secret123"}`)
	for i := 0; i < authLoginFailLimit+2; i++ {
		req, rec := authRequest(http.MethodPost, "/auth/login", body, "192.0.2.7:12345")
		handleAuthLogin(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: status=%d want 200", i+1, rec.Code)
		}
	}
}

func TestAuthRefreshRateLimitPerIP(t *testing.T) {
	mock, cleanup := setupAuthRateLimitTest(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"access_token":"new"}`))
	})
	defer cleanup()

	body := []byte(`{"refresh_token":"rtok"}`)
	for i := 0; i < authRefreshLimit; i++ {
		req, rec := authRequest(http.MethodPost, "/auth/refresh", body, "10.0.0.42:12345")
		handleAuthRefresh(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: status=%d want 200", i+1, rec.Code)
		}
	}

	req, rec := authRequest(http.MethodPost, "/auth/refresh", body, "10.0.0.42:12345")
	handleAuthRefresh(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("missing Retry-After header")
	}
	var payload map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if payload["error"] == "" {
		t.Fatal("expected error message in 429 body")
	}

	_ = mock
}

func TestClientIPFromForwardedHeaders(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Forwarded-For", "203.0.113.1, 10.0.0.1")
	if got := clientIP(r); got != "203.0.113.1" {
		t.Fatalf("X-Forwarded-For: got %q", got)
	}

	r2 := httptest.NewRequest(http.MethodGet, "/", nil)
	r2.RemoteAddr = "127.0.0.1:9999"
	if got := clientIP(r2); got != "127.0.0.1" {
		t.Fatalf("RemoteAddr: got %q", got)
	}
}

func TestMemoryRateBackendIncrementAndCount(t *testing.T) {
	backend := &memoryRateBackend{}
	ctx := context.Background()
	key := "test:key"

	for i := 1; i <= 3; i++ {
		n, _, err := backend.increment(ctx, key, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if n != i {
			t.Fatalf("increment %d: got count %d", i, n)
		}
	}
	count, ttl, err := backend.count(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("count=%d want 3", count)
	}
	if ttl <= 0 {
		t.Fatalf("ttl=%v want positive", ttl)
	}
}
