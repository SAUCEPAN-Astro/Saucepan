package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDoPresignedRequestDoesNotFollowRedirect(t *testing.T) {
	redirectTargetHit := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectTargetHit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	resp, err := doPresignedRequest(http.MethodPost, source.URL, "", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("status=%d, want %d", resp.StatusCode, http.StatusTemporaryRedirect)
	}
	if redirectTargetHit {
		t.Fatal("presigned request followed redirect to another host")
	}
}
