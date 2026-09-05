package shared

import (
	"testing"
	"time"

	"go.uber.org/zap"
)

// TestMetricsFlushNilRedisDoesNotPanic is a regression test for #484: Flush()
// used to call m.rdb.Context() unconditionally when events were buffered, so a
// collector built with a nil Redis client (NewMetricsCollector(nil, ...)) would
// panic on Stop()/Flush() after a single Emit(). refreshGauges() already
// guarded with `if m.rdb != nil`; Flush() now does too.
func TestMetricsFlushNilRedisDoesNotPanic(t *testing.T) {
	m := NewMetricsCollector(nil, zap.NewNop().Sugar(), 0)
	m.Emit(TaskEvent{
		Event:     EventTaskAssigned,
		Timestamp: time.Now().UTC(),
		TaskID:    1,
		Priority:  10,
		NodeID:    "node-a",
	})

	// Both must be no-ops (drop the buffered events) rather than panic.
	m.Flush()
	m.Stop()
}

func TestToObservationIncludesGaugesAndLatencies(t *testing.T) {
	pg := 12.5
	mqtt := 3.0
	total := int64(1500)
	evt := TaskEvent{
		Event:                EventTaskAssigned,
		Timestamp:            time.Now().UTC(),
		TaskID:               7,
		Priority:             100,
		NodeID:               "node-a",
		PGNotifyLatencyMS:    &pg,
		MQTTPublishLatencyMS: &mqtt,
		TimerTotalUs:         &total,
		CampaignID:           "camp-1",
	}
	gauges := hotpathGauges{
		QueueDepth:     4,
		NodesActive:    2,
		StaleTaskCount: 1,
		StreamLen:      10,
		EventCounters:  map[string]int64{"task_assigned:total": 9, "preemptions:total": 2},
		UpdatedAt:      time.Now().UTC(),
	}
	obs := evt.ToObservation(gauges)
	m := obs.Metrics
	if m["ops.pg_notify_latency_ms"] != pg {
		t.Fatalf("pg notify: %v", m["ops.pg_notify_latency_ms"])
	}
	if m["ops.mqtt_publish_latency_ms"] != mqtt {
		t.Fatalf("mqtt publish: %v", m["ops.mqtt_publish_latency_ms"])
	}
	if m["ops.queue_depth"] != int64(4) {
		t.Fatalf("queue_depth: %v", m["ops.queue_depth"])
	}
	if m["ops.nodes_active"] != int64(2) {
		t.Fatalf("nodes_active: %v", m["ops.nodes_active"])
	}
	if m["ops.preemptions_total"] != int64(2) {
		t.Fatalf("preemptions: %v", m["ops.preemptions_total"])
	}
	if m["ops.campaign_id"] != "camp-1" {
		t.Fatalf("campaign: %v", m["ops.campaign_id"])
	}
}

func TestTimerSnapshotUs(t *testing.T) {
	tm := NewTimer("t")
	time.Sleep(2 * time.Millisecond)
	tm.Step("a")
	time.Sleep(2 * time.Millisecond)
	tm.Step("b")
	total, offset, delta := tm.SnapshotUs()
	if total <= 0 || offset <= 0 || delta <= 0 {
		t.Fatalf("snapshot total=%d offset=%d delta=%d", total, offset, delta)
	}
}
