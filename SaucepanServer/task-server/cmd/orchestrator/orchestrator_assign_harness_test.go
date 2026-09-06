package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/saucepan/hotpath/shared"
	"go.uber.org/zap"
)

// orchestrator_assign_harness_test.go — #407 regression harness for the
// post-selection assignment path. Drives selectAssignments (pure) and
// assignOnce (through the assignExec seam, backed by an in-memory fake) with
// no live Postgres / Redis / MQTT and no t.Skipf. The executor-level
// counterparts live in orchestrator_reclaim_test.go (#403 lease reclaim) and
// cmd/collector/collector_nodestate_test.go (#404 node-state ownership).

// fakeExec is an in-memory assignExec. ClaimPrimary is a compare-and-set on
// taskID so concurrent callers race exactly as persistTaskAssignment's
// pending→assigned UPDATE would (#400).
type fakeExec struct {
	mu sync.Mutex

	claimedTasks map[int]bool

	claims         []nodeTask // successful ClaimPrimary calls
	claimTry       int        // every ClaimPrimary call, won or lost
	cohort         []nodeTask
	cohortReleased []nodeTask
	released       []nodeTask // ReleasePreempted
	published      []string   // node IDs passed to PublishAssign, in order
	nodeBusy       []nodeTask
	preemptQ       []taskPrio // PreemptedTaskToQueued
	inflight       []taskPrio // TaskToInflight
	requeued       []taskPrio
	flushes        int

	claimErr                error // when set, ClaimPrimary returns it
	publishErr              error // when set, PublishAssign returns it
	publishCall             int
	publishCallsBeforeError int
}

type nodeTask struct {
	NodeID   string
	TaskID   int
	Priority int
}
type taskPrio struct {
	TaskID   int
	Priority int
}

func newFakeExec() *fakeExec { return &fakeExec{claimedTasks: map[int]bool{}} }

func (f *fakeExec) ClaimPrimary(_ context.Context, taskID int, nodeID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claimTry++
	if f.claimErr != nil {
		return false, f.claimErr
	}
	if f.claimedTasks[taskID] {
		return false, nil
	}
	f.claimedTasks[taskID] = true
	f.claims = append(f.claims, nodeTask{NodeID: nodeID, TaskID: taskID})
	return true, nil
}

func (f *fakeExec) RecordCohort(_ context.Context, taskID int, nodeID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cohort = append(f.cohort, nodeTask{NodeID: nodeID, TaskID: taskID})
	return nil
}

func (f *fakeExec) ReleaseCohort(_ context.Context, taskID int, nodeID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cohortReleased = append(f.cohortReleased, nodeTask{NodeID: nodeID, TaskID: taskID})
	return nil
}

func (f *fakeExec) ReleasePreempted(_ context.Context, prevTaskID int, nodeID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.released = append(f.released, nodeTask{NodeID: nodeID, TaskID: prevTaskID})
	return nil
}

func (f *fakeExec) PublishAssign(_ shared.NotifyPayload, sel shared.SelectorResult) (time.Duration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.published = append(f.published, sel.NodeID)
	f.publishCall++
	if f.publishErr != nil && f.publishCallsBeforeError > 0 && f.publishCall <= f.publishCallsBeforeError {
		return 0, nil
	}
	return 0, f.publishErr
}

func (f *fakeExec) NodeBusy(_ context.Context, nodeID string, taskID, priority int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nodeBusy = append(f.nodeBusy, nodeTask{NodeID: nodeID, TaskID: taskID, Priority: priority})
}

func (f *fakeExec) PreemptedTaskToQueued(_ context.Context, taskID, priority int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.preemptQ = append(f.preemptQ, taskPrio{taskID, priority})
}

func (f *fakeExec) TaskToInflight(_ context.Context, taskID, priority int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inflight = append(f.inflight, taskPrio{taskID, priority})
}

func (f *fakeExec) Requeue(_ context.Context, taskID, priority int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requeued = append(f.requeued, taskPrio{taskID, priority})
}

func (f *fakeExec) Flush(_ context.Context) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.flushes++
}

func harnessMetrics(t *testing.T) *shared.MetricsCollector {
	t.Helper()
	m := shared.NewMetricsCollector(nil, zap.NewNop().Sugar(), 0)
	t.Cleanup(m.Stop)
	return m
}

func selResults(ids ...string) []shared.SelectorResult {
	out := make([]shared.SelectorResult, len(ids))
	for i, id := range ids {
		out[i] = shared.SelectorResult{NodeID: id, IsIdle: true, Reason: "idle"}
	}
	return out
}

// --- #400: two concurrent assigns for one task, exactly one claims ----------

func TestAssignOnce_doubleAssignRaceSingleClaim(t *testing.T) {
	const goroutines = 12
	fx := newFakeExec()
	payload := shared.NotifyPayload{TaskID: 501, Priority: 40}
	sugar := zap.NewNop().Sugar()
	metrics := harnessMetrics(t)
	now := time.Now().UTC()

	var wg sync.WaitGroup
	wins := make([]int, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Each caller independently "selected" its own primary node.
			got := assignOnce(context.Background(), fx, payload,
				selResults(string(rune('a'+i))), nil, metrics, sugar, nil, now, 0)
			if len(got) > 0 {
				wins[i] = 1
			}
		}(i)
	}
	wg.Wait()

	total := 0
	for _, w := range wins {
		total += w
	}
	if total != 1 {
		t.Fatalf("expected exactly one winning assign, got %d", total)
	}
	if fx.claimTry != goroutines {
		t.Fatalf("every caller should attempt the claim: tries=%d want %d", fx.claimTry, goroutines)
	}
	if len(fx.claims) != 1 {
		t.Fatalf("exactly one claim must succeed, got %d", len(fx.claims))
	}
	if len(fx.published) != 1 || len(fx.nodeBusy) != 1 || len(fx.inflight) != 1 {
		t.Fatalf("losers must not publish/mark-busy/inflight: published=%d busy=%d inflight=%d",
			len(fx.published), len(fx.nodeBusy), len(fx.inflight))
	}
	if fx.published[0] != fx.claims[0].NodeID {
		t.Fatalf("published node %q != claimed node %q", fx.published[0], fx.claims[0].NodeID)
	}
}

// --- #402: every cohort member gets a task_assignments row -----------------

func TestAssignOnce_cohortUploadRowsForAllMembers(t *testing.T) {
	fx := newFakeExec()
	payload := shared.NotifyPayload{TaskID: 777, Priority: 30}
	ids := []string{"n0", "n1", "n2", "n3", "n4", "n5", "n6", "n7"}

	got := assignOnce(context.Background(), fx, payload,
		selResults(ids...), nil, harnessMetrics(t), zap.NewNop().Sugar(), nil, time.Now().UTC(), 0)

	if len(got) != 8 {
		t.Fatalf("expected 8 assigned nodes, got %d (%v)", len(got), got)
	}
	// Primary row for n0, cohort rows for n1..n7 → union covers all 8.
	if len(fx.claims) != 1 || fx.claims[0].NodeID != "n0" {
		t.Fatalf("primary claim = %+v, want one row for n0", fx.claims)
	}
	if len(fx.cohort) != 7 {
		t.Fatalf("expected 7 cohort rows, got %d (%+v)", len(fx.cohort), fx.cohort)
	}
	covered := map[string]bool{fx.claims[0].NodeID: true}
	for _, c := range fx.cohort {
		covered[c.NodeID] = true
	}
	for _, id := range ids {
		if !covered[id] {
			t.Fatalf("node %q has no task_assignments row (upload auth would fail)", id)
		}
	}
	if len(fx.nodeBusy) != 8 || len(fx.published) != 8 {
		t.Fatalf("all 8 members should be marked busy + published: busy=%d published=%d",
			len(fx.nodeBusy), len(fx.published))
	}
	if fx.flushes != 1 {
		t.Fatalf("expected a single batched Redis flush, got %d", fx.flushes)
	}
}

// --- #401: drain and NOTIFY entry points share one path -------------------

func TestAssignOnce_drainAndNotifyParity(t *testing.T) {
	ids := []string{"p0", "p1", "p2"}
	payload := shared.NotifyPayload{TaskID: 900, Priority: 25}
	now := time.Now().UTC()

	// NOTIFY entry: a live timer + measured pg-notify latency.
	notifyFx := newFakeExec()
	assignOnce(context.Background(), notifyFx, payload, selResults(ids...), nil,
		harnessMetrics(t), zap.NewNop().Sugar(), shared.NewTimer("task_lifecycle"), now, 4.2)

	// Drain entry: no timer, zero latency (exactly how drainLoop calls in).
	drainFx := newFakeExec()
	assignOnce(context.Background(), drainFx, payload, selResults(ids...), nil,
		harnessMetrics(t), zap.NewNop().Sugar(), nil, now, 0)

	if !sameOps(notifyFx, drainFx) {
		t.Fatalf("drain vs NOTIFY behavioural fork:\n NOTIFY: %s\n DRAIN : %s",
			opsSummary(notifyFx), opsSummary(drainFx))
	}
}

func sameOps(a, b *fakeExec) bool {
	return opsSummary(a) == opsSummary(b)
}

func opsSummary(f *fakeExec) string {
	return "claims=" + join(f.claims) +
		" cohort=" + join(f.cohort) +
		" busy=" + join(f.nodeBusy) +
		" published=" + joinS(f.published) +
		" inflight=" + joinTP(f.inflight) +
		" preemptQ=" + joinTP(f.preemptQ) +
		" released=" + join(f.released) +
		" requeued=" + joinTP(f.requeued)
}

func join(xs []nodeTask) string {
	s := ""
	for _, x := range xs {
		s += x.NodeID + ","
	}
	return s
}
func joinS(xs []string) string {
	s := ""
	for _, x := range xs {
		s += x + ","
	}
	return s
}
func joinTP(xs []taskPrio) string {
	s := ""
	for _, x := range xs {
		s += string(rune('0'+x.TaskID%10)) + ","
	}
	return s
}

// --- preemption bookkeeping + #404 assign-side ownership ------------------

func TestAssignOnce_preemptionReleasesAndRequeuesVictim(t *testing.T) {
	fx := newFakeExec()
	payload := shared.NotifyPayload{TaskID: 42, Priority: 10}
	prev := 7
	assignments := []shared.SelectorResult{
		{NodeID: "anchor", IsIdle: true, Reason: "idle"},
		{NodeID: "victimNode", Preempting: true, PrevTaskID: &prev, Reason: "priority_preempt"},
	}

	got := assignOnce(context.Background(), fx, payload, assignments, nil,
		harnessMetrics(t), zap.NewNop().Sugar(), nil, time.Now().UTC(), 0)

	if len(got) != 2 {
		t.Fatalf("expected 2 assigned nodes, got %v", got)
	}
	if len(fx.released) != 1 || fx.released[0].TaskID != prev || fx.released[0].NodeID != "victimNode" {
		t.Fatalf("victim task %d on victimNode should be released, got %+v", prev, fx.released)
	}
	if len(fx.preemptQ) != 1 || fx.preemptQ[0].TaskID != prev {
		t.Fatalf("preempted task %d should go back to the queue, got %+v", prev, fx.preemptQ)
	}
}

func TestAssignOnce_noBusyWritesWhenClaimLost(t *testing.T) {
	fx := newFakeExec()
	fx.claimedTasks[9] = true // someone already owns task 9

	got := assignOnce(context.Background(), fx, shared.NotifyPayload{TaskID: 9, Priority: 1},
		selResults("n0", "n1"), nil, harnessMetrics(t), zap.NewNop().Sugar(), nil, time.Now().UTC(), 0)

	if got != nil {
		t.Fatalf("claim lost → nil result, got %v", got)
	}
	// #404: orchestrator owns current_task_id; a lost claim must write nothing.
	if len(fx.nodeBusy) != 0 || len(fx.cohort) != 0 || len(fx.published) != 0 || fx.flushes != 0 {
		t.Fatalf("lost claim must not touch node state: %s flushes=%d", opsSummary(fx), fx.flushes)
	}
}

func TestAssignOnce_noEligibleNodeRequeues(t *testing.T) {
	fx := newFakeExec()
	got := assignOnce(context.Background(), fx, shared.NotifyPayload{TaskID: 3, Priority: 2},
		nil, nil, harnessMetrics(t), zap.NewNop().Sugar(), nil, time.Now().UTC(), 0)
	if got != nil {
		t.Fatalf("no assignments → nil, got %v", got)
	}
	if len(fx.requeued) != 1 || fx.requeued[0].TaskID != 3 {
		t.Fatalf("empty selection must requeue the task, got %+v", fx.requeued)
	}
	if len(fx.claims) != 0 {
		t.Fatal("no claim attempt when nothing was selected")
	}
}

func TestAssignOnce_claimErrorRequeues(t *testing.T) {
	fx := newFakeExec()
	fx.claimErr = context.DeadlineExceeded
	got := assignOnce(context.Background(), fx, shared.NotifyPayload{TaskID: 55, Priority: 5},
		selResults("n0"), nil, harnessMetrics(t), zap.NewNop().Sugar(), nil, time.Now().UTC(), 0)
	if got != nil {
		t.Fatalf("claim error → nil, got %v", got)
	}
	if len(fx.requeued) != 1 || fx.requeued[0].TaskID != 55 {
		t.Fatalf("claim error must requeue, got %+v", fx.requeued)
	}
	if len(fx.published) != 0 {
		t.Fatal("claim error must not publish")
	}
}

func TestAssignOnce_publishErrorRetainsClaimForReclaim(t *testing.T) {
	fx := newFakeExec()
	fx.publishErr = errors.New("broker unavailable")
	got := assignOnce(context.Background(), fx, shared.NotifyPayload{TaskID: 56, Priority: 5},
		selResults("n0"), nil, harnessMetrics(t), zap.NewNop().Sugar(), nil, time.Now().UTC(), 0)
	if got != nil {
		t.Fatalf("publish error → nil, got %v", got)
	}
	if len(fx.requeued) != 0 {
		t.Fatalf("ambiguous publish error must not immediately requeue, got %+v", fx.requeued)
	}
	if len(fx.nodeBusy) != 0 || len(fx.inflight) != 1 || fx.flushes != 1 {
		t.Fatalf("publish error must retain task claim for lease reclaim: %s flushes=%d", opsSummary(fx), fx.flushes)
	}
}

func TestAssignOnce_partialPublishRetainsDeliveredClaim(t *testing.T) {
	fx := newFakeExec()
	fx.publishErr = errors.New("broker unavailable")
	fx.publishCallsBeforeError = 1
	assignments := selResults("n0", "n1", "n2")
	got := assignOnce(context.Background(), fx, shared.NotifyPayload{TaskID: 57, Priority: 5},
		assignments, nil, harnessMetrics(t), zap.NewNop().Sugar(), nil, time.Now().UTC(), 0)
	if got != nil {
		t.Fatalf("partial publish error → nil result, got %v", got)
	}
	if len(fx.published) != 2 || fx.published[0] != "n0" || fx.published[1] != "n1" {
		t.Fatalf("expected first delivery and second attempted publish, got %v", fx.published)
	}
	if len(fx.cohortReleased) != 1 || fx.cohortReleased[0].NodeID != "n1" {
		t.Fatalf("failed cohort row must be released, got %+v", fx.cohortReleased)
	}
	if len(fx.nodeBusy) != 1 || fx.nodeBusy[0].NodeID != "n0" || len(fx.inflight) != 1 {
		t.Fatalf("delivered primary must remain active: busy=%+v inflight=%+v", fx.nodeBusy, fx.inflight)
	}
	if len(fx.requeued) != 0 {
		t.Fatalf("partial publish must not requeue, got %+v", fx.requeued)
	}
}

// --- roster-miss fallback in the selection ladder ------------------------

func TestSelectAssignments_rosterMissFallsBackToFullFleet(t *testing.T) {
	now := time.Date(2026, 6, 15, 2, 0, 0, 0, time.UTC)
	req := shared.TaskRequirements{}

	// The full fleet has one eligible idle scope; the roster-narrowed pool is
	// a single offline scope that matches nothing.
	fleetNode := shared.NodeEvaluation{
		NodeID: "fleet-1", Status: shared.NodeStatusIdle, ReliabilityScore: 1.0,
	}
	rosterNode := shared.NodeEvaluation{NodeID: "standby-1", Status: shared.NodeStatusOffline}
	nodes := []shared.NodeEvaluation{fleetNode, rosterNode}
	selectPool := []shared.NodeEvaluation{rosterNode}

	// rosterLen > 0 and selectPool smaller than the fleet → fallback fires.
	got := selectAssignments(nodes, selectPool, 1, req, 10, 5, 0, now)
	if len(got) != 1 || got[0].NodeID != "fleet-1" {
		t.Fatalf("expected full-fleet fallback to pick fleet-1, got %+v", got)
	}

	// No roster (rosterLen 0) → no fallback, the empty pool stays empty.
	if got := selectAssignments(nodes, selectPool, 0, req, 10, 5, 0, now); got != nil {
		t.Fatalf("rosterLen 0 must not trigger the full-fleet fallback, got %+v", got)
	}

	// Roster pool already satisfies the task → no fallback needed.
	okPool := []shared.NodeEvaluation{fleetNode}
	got = selectAssignments(nodes, okPool, 1, req, 10, 5, 0, now)
	if len(got) != 1 || got[0].NodeID != "fleet-1" {
		t.Fatalf("roster hit should be used directly, got %+v", got)
	}
}
