package main

import (
	"context"
	"testing"

	"github.com/saucepan/hotpath/shared"
	"go.uber.org/zap"
)

// fakeNodeStateStore is a map-backed stand-in for the Redis node_state hash,
// so the #404 handoff logic can be exercised without miniredis.
type fakeNodeStateStore struct {
	taskID   string // current_task_id ("" == field absent)
	prio     string // current_task_priority
	status   string
	cleared  bool
	readErr  error
	clearErr error
}

func (f *fakeNodeStateStore) currentTaskID(context.Context, string) (string, error) {
	return f.taskID, f.readErr
}

func (f *fakeNodeStateStore) clearCurrentTask(_ context.Context, _ string, want string) error {
	if f.clearErr != nil {
		return f.clearErr
	}
	if f.taskID != want {
		return nil // a fresher assignment landed — real impl no-ops here too
	}
	f.taskID, f.prio, f.cleared = "", "", true
	f.status = shared.NodeStatusIdle
	return nil
}

// fakeTaskStatus is a stand-in for the tasks table.
type fakeTaskStatus struct {
	byID map[int]string
}

func (f *fakeTaskStatus) taskStatus(_ context.Context, id int) (string, bool, error) {
	s, ok := f.byID[id]
	return s, ok, nil
}

func iptr(i int) *int { return &i }

func TestReconcileNodeState(t *testing.T) {
	log := zap.NewNop().Sugar()
	ctx := context.Background()

	t.Run("idle heartbeat during assign window does not clear current_task_id", func(t *testing.T) {
		store := &fakeNodeStateStore{taskID: "42", prio: "5"}
		tasks := &fakeTaskStatus{byID: map[int]string{42: shared.TaskStatusAssigned}}
		tel := shared.Telemetry{NodeID: "n1", Status: "idle"} // CurrentTaskID nil
		reconcileNodeState(ctx, store, tasks, "n1", tel, log)
		if store.cleared || store.taskID != "42" {
			t.Fatalf("assignment marker cleared during the assign window: %+v", store)
		}
	})

	t.Run("in_progress task is not cleared", func(t *testing.T) {
		store := &fakeNodeStateStore{taskID: "42", prio: "5"}
		tasks := &fakeTaskStatus{byID: map[int]string{42: shared.TaskStatusInProgress}}
		tel := shared.Telemetry{NodeID: "n1", Status: "idle"}
		reconcileNodeState(ctx, store, tasks, "n1", tel, log)
		if store.cleared {
			t.Fatal("marker cleared for a task still in_progress")
		}
	})

	t.Run("DB-confirmed completion clears", func(t *testing.T) {
		store := &fakeNodeStateStore{taskID: "42", prio: "5"}
		tasks := &fakeTaskStatus{byID: map[int]string{42: shared.TaskStatusCompleted}}
		tel := shared.Telemetry{NodeID: "n1", Status: "idle"}
		reconcileNodeState(ctx, store, tasks, "n1", tel, log)
		if !store.cleared || store.taskID != "" {
			t.Fatalf("marker not cleared after DB-confirmed completion: %+v", store)
		}
	})

	t.Run("expired task clears", func(t *testing.T) {
		store := &fakeNodeStateStore{taskID: "42", prio: "5"}
		tasks := &fakeTaskStatus{byID: map[int]string{42: shared.TaskStatusExpired}}
		tel := shared.Telemetry{NodeID: "n1", Status: "idle"}
		reconcileNodeState(ctx, store, tasks, "n1", tel, log)
		if !store.cleared {
			t.Fatal("marker not cleared for an expired task")
		}
	})

	t.Run("telemetry carrying its own task id never clears", func(t *testing.T) {
		store := &fakeNodeStateStore{taskID: "42", prio: "5"}
		tasks := &fakeTaskStatus{byID: map[int]string{42: shared.TaskStatusCompleted}}
		tel := shared.Telemetry{NodeID: "n1", Status: "idle", CurrentTaskID: iptr(42)}
		reconcileNodeState(ctx, store, tasks, "n1", tel, log)
		if store.cleared {
			t.Fatal("cleared despite telemetry still reporting a current_task_id")
		}
	})

	t.Run("busy heartbeat never clears", func(t *testing.T) {
		store := &fakeNodeStateStore{taskID: "42", prio: "5"}
		tasks := &fakeTaskStatus{byID: map[int]string{42: shared.TaskStatusCompleted}}
		tel := shared.Telemetry{NodeID: "n1", Status: "observing"}
		reconcileNodeState(ctx, store, tasks, "n1", tel, log)
		if store.cleared {
			t.Fatal("cleared on a non-idle heartbeat")
		}
	})

	t.Run("unknown task is left for the lease-reclaim backstop", func(t *testing.T) {
		store := &fakeNodeStateStore{taskID: "99", prio: "1"}
		tasks := &fakeTaskStatus{byID: map[int]string{}}
		tel := shared.Telemetry{NodeID: "n1", Status: "idle"}
		reconcileNodeState(ctx, store, tasks, "n1", tel, log)
		if store.cleared {
			t.Fatal("cleared a task Postgres has no row for")
		}
	})

	t.Run("no assignment marker is a no-op", func(t *testing.T) {
		store := &fakeNodeStateStore{taskID: ""}
		tasks := &fakeTaskStatus{byID: map[int]string{}}
		tel := shared.Telemetry{NodeID: "n1", Status: "idle"}
		reconcileNodeState(ctx, store, tasks, "n1", tel, log)
		if store.cleared {
			t.Fatal("clear attempted with no marker present")
		}
	})

	// NOTIFY-replay: the orchestrator re-delivers an assignment NOTIFY right
	// after a single idle blip. Because the idle blip does NOT clear
	// current_task_id (the task is still 'assigned'), the marker the
	// orchestrator's selector reads as "busy" survives, so the replay cannot
	// produce a second assignment. Model two idle beats and assert survival.
	t.Run("idle blip then replay cannot double-assign", func(t *testing.T) {
		store := &fakeNodeStateStore{taskID: "42", prio: "5"}
		tasks := &fakeTaskStatus{byID: map[int]string{42: shared.TaskStatusAssigned}}
		idle := shared.Telemetry{NodeID: "n1", Status: "idle"}
		reconcileNodeState(ctx, store, tasks, "n1", idle, log)
		reconcileNodeState(ctx, store, tasks, "n1", idle, log)
		if store.cleared || store.taskID != "42" {
			t.Fatalf("assignment marker lost across an idle blip — double-assign window still open: %+v", store)
		}
	})
}
