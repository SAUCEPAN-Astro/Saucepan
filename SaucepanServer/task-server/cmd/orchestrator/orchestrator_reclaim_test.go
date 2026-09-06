package main

import (
	"testing"
	"time"
)

// fakeClock is a movable clock for the reclaim path. Assigning reclaimNow to
// its Now method lets tests drive lease expiry / backoff deterministically.
type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func withFakeClock(t *testing.T, start time.Time) *fakeClock {
	t.Helper()
	c := &fakeClock{t: start}
	prev := reclaimNow
	reclaimNow = c.Now
	t.Cleanup(func() { reclaimNow = prev })
	return c
}

func withLeaseCfg(t *testing.T, cfg leaseConfig) {
	t.Helper()
	prev := leaseCfg
	leaseCfg = cfg
	t.Cleanup(func() { leaseCfg = prev })
}

func TestLeaseExpired(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)

	if leaseExpired(time.Time{}, now) {
		t.Fatal("a NULL/zero lease must not count as expired")
	}
	if leaseExpired(now.Add(1*time.Minute), now) {
		t.Fatal("a lease in the future is not expired")
	}
	if !leaseExpired(now.Add(-1*time.Second), now) {
		t.Fatal("a lease one second in the past is expired")
	}

	// A renewed lease is not reclaimed: renewal pushes lease_expires_at to
	// now()+TTL, so even well after the original grant it stays in the future.
	clk := withFakeClock(t, now)
	withLeaseCfg(t, leaseConfig{TTL: 15 * time.Minute})
	granted := leaseExpiry() // now+15m
	clk.advance(14 * time.Minute)
	renewed := leaseExpiry() // (now+14m)+15m
	clk.advance(2 * time.Minute)
	if leaseExpired(renewed, reclaimNow()) {
		t.Fatal("a lease renewed 14m in is not expired 16m after the original grant")
	}
	if !leaseExpired(granted, reclaimNow()) {
		t.Fatal("the un-renewed original grant is expired 16m in")
	}
}

func TestBackoffWindow(t *testing.T) {
	base := 2 * time.Minute
	cases := []struct {
		failure int
		want    time.Duration
	}{
		{0, 2 * time.Minute},
		{1, 4 * time.Minute},
		{2, 8 * time.Minute},
		{3, 16 * time.Minute},
		{4, 30 * time.Minute},  // 32m capped
		{10, 30 * time.Minute}, // capped
		{999, 30 * time.Minute},
		{-1, 2 * time.Minute}, // negative treated as 0
	}
	for _, tc := range cases {
		if got := backoffWindow(base, tc.failure); got != tc.want {
			t.Fatalf("backoffWindow(2m, %d) = %s, want %s", tc.failure, got, tc.want)
		}
	}
	if got := backoffWindow(0, 3); got != 0 {
		t.Fatalf("backoffWindow(0, _) = %s, want 0 (disabled)", got)
	}
}

func TestTaskInBackoff_BlocksImmediateReselect(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	base := 2 * time.Minute

	// Never attempted → not in backoff.
	if taskInBackoff(time.Time{}, base, 0, now) {
		t.Fatal("zero lastAttempt must not be in backoff")
	}

	// Just requeued (failure_count 1 → 4m window): immediate re-select blocked.
	last := now
	if !taskInBackoff(last, base, 1, now.Add(1*time.Second)) {
		t.Fatal("a task requeued 1s ago with failure_count=1 must be in backoff")
	}
	if !taskInBackoff(last, base, 1, now.Add(3*time.Minute)) {
		t.Fatal("still in backoff 3m into a 4m window")
	}
	// Window elapsed → selectable again.
	if taskInBackoff(last, base, 1, now.Add(4*time.Minute+time.Second)) {
		t.Fatal("backoff must clear once the 4m window elapses")
	}

	// base<=0 disables backoff entirely (matches zero-value leaseCfg in tests).
	if taskInBackoff(now, 0, 5, now.Add(time.Second)) {
		t.Fatal("backoff disabled when base<=0")
	}
}

func TestLeaseReclaimTerminal(t *testing.T) {
	if leaseReclaimTerminal(4, 5) {
		t.Fatal("4 failures < max 5 is not terminal")
	}
	if !leaseReclaimTerminal(5, 5) {
		t.Fatal("5 failures == max 5 is terminal")
	}
	if !leaseReclaimTerminal(9, 5) {
		t.Fatal("past max is terminal")
	}
	if leaseReclaimTerminal(100, 0) {
		t.Fatal("max_failures<=0 disables the terminal cutoff")
	}
}

func TestDecideReclaim(t *testing.T) {
	// Expired lease, no assignees left, under the failure cap → requeue with a
	// bumped failure count.
	act := decideReclaim(0 /*activeLeft*/, 1 /*newFailure*/, 5 /*max*/)
	if !act.RequeueTask || act.TerminalExpire {
		t.Fatalf("first lapse should requeue, got %+v", act)
	}

	// A surviving cohort assignee → the task is untouched, only the dead row
	// is expired by the caller.
	act = decideReclaim(2, 3, 5)
	if act.RequeueTask || act.TerminalExpire {
		t.Fatalf("cohort member death must not requeue/expire the task, got %+v", act)
	}

	// Reaching MAX_FAILURES → terminal expired, never requeued.
	act = decideReclaim(0, 5, 5)
	if act.RequeueTask || !act.TerminalExpire {
		t.Fatalf("MAX_FAILURES lapse should terminally expire, got %+v", act)
	}
	act = decideReclaim(0, 6, 5)
	if act.RequeueTask || !act.TerminalExpire {
		t.Fatalf("past MAX_FAILURES stays terminal, got %+v", act)
	}
}

// TestReclaimLifecycle_FakeClock walks one task through repeated lease loss on
// a fake clock: each lapse requeues and bumps failure_count while backoff
// blocks an immediate retry, until MAX_FAILURES makes it terminal.
func TestReclaimLifecycle_FakeClock(t *testing.T) {
	start := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	clk := withFakeClock(t, start)
	cfg := leaseConfig{
		TTL:         15 * time.Minute,
		Interval:    time.Minute,
		BackoffBase: 2 * time.Minute,
		MaxFailures: 3,
	}
	withLeaseCfg(t, cfg)

	failure := 0
	for attempt := 1; ; attempt++ {
		lease := leaseExpiry() // assigned/renewed now
		lastAttempt := reclaimNow()

		// Node dies; TTL elapses with no renewal.
		clk.advance(cfg.TTL + time.Second)
		if !leaseExpired(lease, reclaimNow()) {
			t.Fatalf("attempt %d: lease should be expired after TTL", attempt)
		}

		failure++
		act := decideReclaim(0, failure, cfg.MaxFailures)

		// Immediately after the requeue the task is inside its backoff window.
		if !act.TerminalExpire &&
			!taskInBackoff(reclaimNow(), cfg.BackoffBase, failure, reclaimNow().Add(time.Second)) {
			t.Fatalf("attempt %d: task must be in backoff right after requeue", attempt)
		}

		if failure >= cfg.MaxFailures {
			if !act.TerminalExpire || act.RequeueTask {
				t.Fatalf("attempt %d: failure_count=%d should be terminal, got %+v", attempt, failure, act)
			}
			break
		}
		if !act.RequeueTask || act.TerminalExpire {
			t.Fatalf("attempt %d: failure_count=%d should requeue, got %+v", attempt, failure, act)
		}

		// Wait out the backoff, then the task is selectable and gets reassigned.
		clk.advance(backoffWindow(cfg.BackoffBase, failure) + time.Second)
		if taskInBackoff(lastAttempt, cfg.BackoffBase, failure, reclaimNow()) {
			t.Fatalf("attempt %d: backoff should have cleared", attempt)
		}
	}
	if failure != cfg.MaxFailures {
		t.Fatalf("expected exactly MAX_FAILURES=%d lapses before terminal, got %d", cfg.MaxFailures, failure)
	}
}
