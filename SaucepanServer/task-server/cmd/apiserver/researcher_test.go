package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequireApprovedResearcher_NoClaims(t *testing.T) {
	err := requireApprovedResearcher(context.Background())
	if err == nil {
		t.Fatal("expected error without claims")
	}
	if err.Error() != "authentication required" {
		t.Fatalf("got %q", err.Error())
	}
}

func TestRequireResearcherJWT_Unauthenticated(t *testing.T) {
	called := false
	handler := requireResearcherJWT(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/campaigns", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if called {
		t.Fatal("handler should not run")
	}
}

func TestRequireResearcherJWT_NotApproved(t *testing.T) {
	setupResearcherTestDB(t)
	ctx := context.Background()
	userID := insertTestUser(t, ctx, "not-approved@example.com", false)

	access, _, err := generateAccessToken(userID, "not-approved@example.com")
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	called := false
	handler := requireResearcherJWT(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/campaigns", nil)
	req.Header.Set("Authorization", "Bearer "+access)
	rec := httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if called {
		t.Fatal("handler should not run")
	}
}

func TestRequireResearcherJWT_Approved(t *testing.T) {
	setupResearcherTestDB(t)
	ctx := context.Background()
	userID := insertTestUser(t, ctx, "approved@example.com", true)

	access, _, err := generateAccessToken(userID, "approved@example.com")
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	called := false
	handler := requireResearcherJWT(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/campaigns", nil)
	req.Header.Set("Authorization", "Bearer "+access)
	rec := httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !called {
		t.Fatal("handler should run for approved researcher")
	}
}

// TestResearcherSurfaceRoutesGatedByApproval covers #470 item 7: the
// alerts / updates / inbox JWT routes used to run on bare requireJWT, so an
// un-approved account could still poll them. They now go through
// requireResearcherJWT — an un-approved caller must get 403 before the
// handler body runs.
func TestResearcherSurfaceRoutesGatedByApproval(t *testing.T) {
	setupResearcherTestDB(t)
	ctx := context.Background()
	userID := insertTestUser(t, ctx, "surface-unapproved@example.com", false)
	access, _, err := generateAccessToken(userID, "surface-unapproved@example.com")
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	cases := []struct {
		name    string
		method  string
		target  string
		handler http.HandlerFunc
	}{
		{"alerts list", http.MethodGet, "/api/v1/alerts", handleListResearcherEvents(eventKindAlert)},
		{"updates list", http.MethodGet, "/api/v1/updates", handleListResearcherEvents(eventKindUpdate)},
		{"alert ack", http.MethodPost, "/api/v1/alerts/x/ack", handleAckResearcherEvent(eventKindAlert)},
		{"inbox poll", http.MethodGet, "/api/v1/inbox", handleInboxPoll},
		{"inbox ack", http.MethodPost, "/api/v1/inbox/x/ack", handleInboxAck},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.target, nil)
			req.Header.Set("Authorization", "Bearer "+access)
			rec := httptest.NewRecorder()
			tc.handler(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status=%d body=%s (want 403 for un-approved)", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestCreateAPIKeyForbiddenForUnapproved locks the API-key issuance gate
// (developer.go) to the shared errResearcherNotApproved string so the SDK
// sees one stable message across every capability route.
func TestCreateAPIKeyForbiddenForUnapproved(t *testing.T) {
	setupResearcherTestDB(t)
	ctx := context.Background()
	userID := insertTestUser(t, ctx, "keys-unapproved@example.com", false)
	access, _, err := generateAccessToken(userID, "keys-unapproved@example.com")
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	body := strings.NewReader(`{"name":"ci","scopes":["tasks:read"]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/keys", body)
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	requireJWT(handleCreateAPIKey)(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s (want 403)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), errResearcherNotApproved.Error()) {
		t.Fatalf("body %q missing %q", rec.Body.String(), errResearcherNotApproved.Error())
	}
}

func TestRequireApprovedResearcher_ApprovedFlag(t *testing.T) {
	setupResearcherTestDB(t)
	ctx := context.Background()
	userID := insertTestUser(t, ctx, "flag@example.com", true)

	claims := &authClaims{UserID: userID, Email: "flag@example.com"}
	ctx = context.WithValue(ctx, userClaimsKey, claims)
	if err := requireApprovedResearcher(ctx); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}

	claims.UserID = insertTestUser(t, ctx, "denied@example.com", false)
	ctx = context.WithValue(ctx, userClaimsKey, claims)
	err := requireApprovedResearcher(ctx)
	if !errors.Is(err, errResearcherNotApproved) {
		t.Fatalf("expected errResearcherNotApproved, got %v", err)
	}
}
