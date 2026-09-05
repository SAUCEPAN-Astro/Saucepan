package main

import (
	"crypto/subtle"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// secretEqual reports whether got equals want without leaking the length of the
// shared prefix through timing. Use for every static bearer/ingest/worker token
// check (JWT verification already uses a MAC and is fine as-is).
func secretEqual(got, want string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// ── JSON helpers ───────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// decodeJSON decodes the request body JSON into dst. Callers are responsible
// for writing the error response on failure.
func decodeJSON(r *http.Request, dst any) error {
	return json.NewDecoder(r.Body).Decode(dst)
}

// ── CORS Middleware ─────────────────────────────────────────────────────

// Default localhost origins when DEV_MODE=1 and CORS_ORIGINS is unset.
// Production defaults to empty (no ACAO) — fail closed. Refs #287.
var corsDevDefaultOrigins = []string{
	"http://localhost:3000",
	"http://localhost:5173",
	"http://localhost:1420",
	"http://127.0.0.1:3000",
	"http://127.0.0.1:5173",
	"http://127.0.0.1:1420",
}

const corsAllowMethods = "GET, POST, PATCH, PUT, DELETE, OPTIONS"
const corsAllowHeaders = "Content-Type, Authorization, X-Request-ID, If-None-Match"
const corsExposeHeaders = "X-Request-ID, X-RateLimit-Remaining, X-RateLimit-Reset"

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

// corsOriginsAllowlist returns explicit origins from CORS_ORIGINS (comma-separated).
// Empty CORS_ORIGINS: deny all browser CORS unless DEV_MODE=1 (localhost defaults).
// Never returns "*" — fail closed for JWT-bearing APIs (#287).
func corsOriginsAllowlist() map[string]struct{} {
	raw := strings.TrimSpace(os.Getenv("CORS_ORIGINS"))
	out := make(map[string]struct{})
	if raw != "" {
		for _, part := range strings.Split(raw, ",") {
			o := strings.TrimSpace(part)
			if o == "" || o == "*" {
				continue
			}
			out[o] = struct{}{}
		}
		return out
	}
	if os.Getenv("DEV_MODE") == "1" {
		for _, o := range corsDevDefaultOrigins {
			out[o] = struct{}{}
		}
	}
	return out
}

func corsAllowedOrigin(origin string, allowlist map[string]struct{}) (string, bool) {
	if origin == "" {
		return "", false
	}
	if _, ok := allowlist[origin]; ok {
		return origin, true
	}
	return "", false
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowlist := corsOriginsAllowlist()
		origin := r.Header.Get("Origin")
		if allowed, ok := corsAllowedOrigin(origin, allowlist); ok {
			w.Header().Set("Access-Control-Allow-Origin", allowed)
			w.Header().Add("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", corsAllowMethods)
			w.Header().Set("Access-Control-Allow-Headers", corsAllowHeaders)
			w.Header().Set("Access-Control-Expose-Headers", corsExposeHeaders)
			w.Header().Set("Access-Control-Max-Age", "86400")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(rw, r)
		log.Printf("[%s] %s %s %d %s", r.Method, r.URL.Path, r.RemoteAddr, rw.status, time.Since(start))
	})
}
