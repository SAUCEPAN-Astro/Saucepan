package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestQuestTaskCreateRequiresResearcher(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/quest/tasks", nil)
	rec := httptest.NewRecorder()
	requireResearcherJWT(handleCreateTask)(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated create: got %d want 401", rec.Code)
	}
}

func TestQuestTaskPatchRequiresResearcher(t *testing.T) {
	req := httptest.NewRequest(http.MethodPatch, "/quest/tasks/1", nil)
	rec := httptest.NewRecorder()
	requireResearcherJWT(handlePatchTask)(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated patch: got %d want 401", rec.Code)
	}
}

func TestQuestTaskCompleteRequiresResearcher(t *testing.T) {
	req := httptest.NewRequest(http.MethodPatch, "/quest/tasks/1/complete", nil)
	rec := httptest.NewRecorder()
	requireResearcherJWT(handleCompleteTask)(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated complete: got %d want 401", rec.Code)
	}
}
