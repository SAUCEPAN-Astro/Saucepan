package main

import (
	"sync"
	"testing"
	"time"
)

func TestGetTelemetrySnapshotMissing(t *testing.T) {
	telemetryMu.Lock()
	delete(telemetryCache, "missing-telescope")
	telemetryMu.Unlock()

	snap, ok := getTelemetrySnapshot("missing-telescope")
	if ok {
		t.Fatalf("expected ok=false for missing telescope, got snap=%+v", snap)
	}
}

func TestGetTelemetrySnapshotFound(t *testing.T) {
	id := "telescope-telemetry-present"
	now := time.Now().UTC()
	want := telemetrySnapshot{TelescopeID: id, Status: "idle", LastUpdateAt: now}

	telemetryMu.Lock()
	telemetryCache[id] = want
	telemetryMu.Unlock()
	t.Cleanup(func() {
		telemetryMu.Lock()
		delete(telemetryCache, id)
		telemetryMu.Unlock()
	})

	got, ok := getTelemetrySnapshot(id)
	if !ok {
		t.Fatal("expected ok=true for present telescope")
	}
	if got.TelescopeID != id || got.Status != "idle" {
		t.Fatalf("got %+v, want telescope_id=%q status=idle", got, id)
	}
}

// TestTelemetryCacheConcurrentAccess exercises the RWMutex guarding
// telemetryCache under concurrent readers and writers. Run with -race to
// catch any unguarded access.
func TestTelemetryCacheConcurrentAccess(t *testing.T) {
	const workers = 32
	const iterations = 200
	ids := []string{"tele-a", "tele-b", "tele-c"}

	t.Cleanup(func() {
		telemetryMu.Lock()
		for _, id := range ids {
			delete(telemetryCache, id)
		}
		telemetryMu.Unlock()
	})

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			id := ids[worker%len(ids)]
			for i := 0; i < iterations; i++ {
				if i%2 == 0 {
					telemetryMu.Lock()
					telemetryCache[id] = telemetrySnapshot{TelescopeID: id, FileCount: i}
					telemetryMu.Unlock()
				} else {
					_, _ = getTelemetrySnapshot(id)
				}
			}
		}(w)
	}
	wg.Wait()
}
