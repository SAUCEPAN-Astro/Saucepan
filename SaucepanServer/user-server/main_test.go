package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleHealth(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	handleHealth(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if body["status"] != "healthy" || body["service"] != "user-server" {
		t.Fatalf("unexpected health body: %+v", body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected application/json content-type, got %q", ct)
	}
}

func TestHandlePhase2NotImplemented(t *testing.T) {
	paths := []string{"/auth/verify-email", "/auth/forgot-password", "/auth/reset-password"}
	for _, p := range paths {
		req := httptest.NewRequest(http.MethodPost, p, nil)
		rec := httptest.NewRecorder()
		handlePhase2NotImplemented(rec, req)
		if rec.Code != 501 {
			t.Fatalf("%s: expected 501, got %d", p, rec.Code)
		}
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if body["error"] == "" {
			t.Fatalf("expected non-empty error message for %s", p)
		}
	}
}

func TestWriteJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, 202, map[string]int{"a": 1})
	if rec.Code != 202 {
		t.Fatalf("expected 202, got %d", rec.Code)
	}
	var out map[string]int
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out["a"] != 1 {
		t.Fatalf("unexpected body: %+v", out)
	}
}

func TestWriteError(t *testing.T) {
	rec := httptest.NewRecorder()
	writeError(rec, 400, "bad request")
	if rec.Code != 400 {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	var out map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out["error"] != "bad request" {
		t.Fatalf("unexpected body: %+v", out)
	}
}

func TestDecodeJSON_Malformed(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"a":`))
	var dst map[string]any
	if err := decodeJSON(req, &dst); err == nil {
		t.Fatal("expected error decoding malformed JSON")
	}
}

func TestDecodeJSON_Valid(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"a":1}`))
	var dst map[string]any
	if err := decodeJSON(req, &dst); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dst["a"] != float64(1) {
		t.Fatalf("unexpected decode result: %+v", dst)
	}
}

func TestWithTimeout(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	ctx, cancel := withTimeout(req)
	defer cancel()
	if _, ok := ctx.Deadline(); !ok {
		t.Fatal("expected a deadline on the derived context")
	}
}
