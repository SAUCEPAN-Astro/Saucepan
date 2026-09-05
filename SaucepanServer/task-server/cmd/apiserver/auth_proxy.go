package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// userServiceURL is the internal user-server base (no trailing slash).
// Empty → identity handlers return 503 (misconfigured deploy).
var userServiceURL = strings.TrimRight(os.Getenv("USER_SERVICE_URL"), "/")

func proxyToUserService(w http.ResponseWriter, r *http.Request) int {
	if userServiceURL == "" {
		writeError(w, 503, "USER_SERVICE_URL not configured — identity is served by user-server")
		return 503
	}

	target := userServiceURL + r.URL.Path
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}

	ctx := r.Context()
	req, err := http.NewRequestWithContext(ctx, r.Method, target, r.Body)
	if err != nil {
		log.Printf("auth proxy build request: %v", err)
		writeError(w, 502, "Identity service unavailable")
		return 502
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	if auth := r.Header.Get("Authorization"); auth != "" {
		req.Header.Set("Authorization", auth)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("auth proxy to user-server: %v", err)
		writeError(w, 502, "Identity service unavailable")
		return 502
	}
	defer resp.Body.Close()

	for k, vals := range resp.Header {
		if strings.EqualFold(k, "Content-Length") {
			continue
		}
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
	return resp.StatusCode
}

func handleAuthRegister(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ip := clientIP(r)
	key := authRateLimitKey("register", ip)

	if limited, retry := authLimiter.isLimited(ctx, key, authRegisterLimit); limited {
		writeRateLimited(w, retry)
		return
	}
	if limited, retry := authLimiter.recordHit(ctx, key, authRegisterLimit, authRegisterWindow); limited {
		writeRateLimited(w, retry)
		return
	}

	proxyToUserService(w, r)
}

func handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, 400, "Invalid request body")
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))

	var creds struct {
		Username string `json:"username"`
	}
	_ = json.Unmarshal(body, &creds)
	username := normalizeAuthUsername(creds.Username)

	ctx := r.Context()
	ip := clientIP(r)
	key := authRateLimitKey("login", ip, username)

	if limited, retry := authLimiter.isLimited(ctx, key, authLoginFailLimit); limited {
		writeRateLimited(w, retry)
		return
	}

	status := proxyToUserService(w, r)
	if status == http.StatusUnauthorized {
		authLimiter.recordHit(ctx, key, authLoginFailLimit, authLoginFailWindow)
	}
}

func handleAuthRefresh(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ip := clientIP(r)
	key := authRateLimitKey("refresh", ip)

	if limited, retry := authLimiter.isLimited(ctx, key, authRefreshLimit); limited {
		writeRateLimited(w, retry)
		return
	}
	if limited, retry := authLimiter.recordHit(ctx, key, authRefreshLimit, authRefreshWindow); limited {
		writeRateLimited(w, retry)
		return
	}

	proxyToUserService(w, r)
}

func handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	proxyToUserService(w, r)
}

func handleAuthChangePassword(w http.ResponseWriter, r *http.Request) {
	proxyToUserService(w, r)
}

func handleAuthVerifyEmail(w http.ResponseWriter, r *http.Request) {
	proxyToUserService(w, r)
}

func handleAuthForgotPassword(w http.ResponseWriter, r *http.Request) {
	proxyToUserService(w, r)
}

func handleAuthResetPassword(w http.ResponseWriter, r *http.Request) {
	proxyToUserService(w, r)
}
