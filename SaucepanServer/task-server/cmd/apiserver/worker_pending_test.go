package main

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequireWorkerAuthMissingTokenReturns503(t *testing.T) {
	t.Setenv("WORKER_TOKEN", "")
	t.Setenv("DEV_MODE", "")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/worker/pending", nil)
	rec := httptest.NewRecorder()
	handleWorkerPending(rec, req)
	if rec.Code != 503 {
		t.Fatalf("missing token: status=%d want 503 body=%s", rec.Code, rec.Body.String())
	}
}

func TestRequireWorkerAuthWrongBearerReturns401(t *testing.T) {
	t.Setenv("WORKER_TOKEN", "secret-token")
	t.Setenv("DEV_MODE", "")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/worker/pending", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rec := httptest.NewRecorder()
	handleWorkerPending(rec, req)
	if rec.Code != 401 {
		t.Fatalf("bad bearer: status=%d want 401", rec.Code)
	}
}

func TestRequireWorkerAuthDevModeAllowsEmptyToken(t *testing.T) {
	t.Setenv("WORKER_TOKEN", "")
	t.Setenv("DEV_MODE", "1")
	origDB := db
	db = nil
	t.Cleanup(func() { db = origDB })
	req := httptest.NewRequest(http.MethodGet, "/api/v1/worker/pending", nil)
	rec := httptest.NewRecorder()
	handleWorkerPending(rec, req)
	if rec.Code != 200 {
		t.Fatalf("DEV_MODE empty token: status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
}

func TestRequireWorkerEnqueueAuth(t *testing.T) {
	t.Setenv("WORKER_TOKEN", "secret-token")
	t.Setenv("DEV_MODE", "")
	body := bytes.NewBufferString(`{"task_id":1,"campaign_id":1,"object_key":"a/b.fits"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/worker/enqueue", body)
	rec := httptest.NewRecorder()
	handleWorkerEnqueue(rec, req)
	if rec.Code != 401 {
		t.Fatalf("enqueue no auth: status=%d want 401", rec.Code)
	}
}

func TestAssertWorkerTokenConfigured(t *testing.T) {
	t.Setenv("WORKER_TOKEN", "")
	t.Setenv("DEV_MODE", "")
	if err := assertWorkerTokenConfigured(); err == nil {
		t.Fatal("expected error when token missing outside DEV_MODE")
	}
	t.Setenv("DEV_MODE", "1")
	if err := assertWorkerTokenConfigured(); err != nil {
		t.Fatalf("DEV_MODE should allow missing token: %v", err)
	}
	t.Setenv("WORKER_TOKEN", "x")
	t.Setenv("DEV_MODE", "")
	if err := assertWorkerTokenConfigured(); err != nil {
		t.Fatalf("token set should allow boot: %v", err)
	}
}

func TestWorkerPendingMaxDefault(t *testing.T) {
	t.Setenv("WORKER_PENDING_MAX", "")
	if got := workerPendingMax(); got != 1000 {
		t.Fatalf("default max=%d want 1000", got)
	}
	t.Setenv("WORKER_PENDING_MAX", "3")
	if got := workerPendingMax(); got != 3 {
		t.Fatalf("env max=%d want 3", got)
	}
}

func TestEnqueueWorkerJobMemoryOverflow(t *testing.T) {
	t.Setenv("WORKER_PENDING_MAX", "2")
	origDB := db
	db = nil
	t.Cleanup(func() {
		db = origDB
		workerJobsMu.Lock()
		workerJobs = nil
		workerJobsMu.Unlock()
	})
	workerJobsMu.Lock()
	workerJobs = []workerPendingJob{{TaskID: 1, ObjectKey: "a"}}
	workerJobsMu.Unlock()

	err := enqueueWorkerJob(workerPendingJob{TaskID: 2, ObjectKey: "b"})
	if err != nil {
		t.Fatalf("first enqueue: %v", err)
	}
	err = enqueueWorkerJob(workerPendingJob{TaskID: 3, ObjectKey: "c"})
	if !errors.Is(err, errWorkerPendingFull) {
		t.Fatalf("expected queue full, got %v", err)
	}
}

func TestEnqueueWorkerJobMemoryGroupsStackFrames(t *testing.T) {
	origDB := db
	db = nil
	t.Cleanup(func() {
		db = origDB
		workerJobsMu.Lock()
		workerJobs = nil
		workerJobsMu.Unlock()
	})

	if err := enqueueWorkerJob(workerPendingJob{
		TaskID: 7, ProductMode: "stack", ObjectKey: "c/7/a.fits",
	}); err != nil {
		t.Fatal(err)
	}
	if err := enqueueWorkerJob(workerPendingJob{
		TaskID: 7, ProductMode: "stack", ObjectKey: "c/7/b.fits",
	}); err != nil {
		t.Fatal(err)
	}

	workerJobsMu.Lock()
	defer workerJobsMu.Unlock()
	if len(workerJobs) != 1 || len(workerJobs[0].ObjectKeys) != 2 {
		t.Fatalf("stack queue=%+v", workerJobs)
	}
}
