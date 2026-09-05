package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/saucepan/hotpath/shared/wire"
)

// TestStatusRowJSONShape verifies the `saucepan status --json` contract
// (statusRow, status.go): field names and omitempty behavior other tooling
// composes with via jq (§1 of the CLI doc comment).
func TestStatusRowJSONShape(t *testing.T) {
	// Bare row — no telemetry, no retained status, no metadata.
	bare := statusRow{NodeID: "pier_01", Presence: "waiting", ObservedWindowS: 5}
	raw, err := json.Marshal(bare)
	if err != nil {
		t.Fatalf("marshal bare statusRow: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, absent := range []string{"status_retained", "telemetry", "quality_tier"} {
		if _, ok := m[absent]; ok {
			t.Errorf("expected omitempty field %q absent for a bare row, got %v", absent, m[absent])
		}
	}
	for _, present := range []string{"node_id", "presence", "observed_window_s"} {
		if _, ok := m[present]; !ok {
			t.Errorf("expected field %q present, got %v", present, m)
		}
	}

	// Fully populated row.
	taskID := 42
	full := statusRow{
		NodeID:          "pier_02",
		Presence:        "live",
		StatusRetained:  "online",
		Telemetry:       &wire.Telemetry{NodeID: "pier_02", Status: "observing", CurrentTaskID: &taskID},
		QualityTier:     "gold",
		ObservedWindowS: 5.0,
	}
	raw2, err := json.Marshal(full)
	if err != nil {
		t.Fatalf("marshal full statusRow: %v", err)
	}
	var m2 map[string]any
	if err := json.Unmarshal(raw2, &m2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m2["status_retained"] != "online" || m2["quality_tier"] != "gold" {
		t.Fatalf("populated fields missing: %v", m2)
	}
	tel, ok := m2["telemetry"].(map[string]any)
	if !ok {
		t.Fatalf("telemetry not embedded as an object: %v", m2["telemetry"])
	}
	if tel["current_task_id"] != float64(42) {
		t.Fatalf("nested telemetry field wrong: %v", tel)
	}
}

// TestConstraintsViewJSONShape covers `saucepan constraints --json`.
func TestConstraintsViewJSONShape(t *testing.T) {
	minimal := constraintsView{NodeID: "pier_01", Power: 0.5, QualityTier: "standard"}
	raw, err := json.Marshal(minimal)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, absent := range []string{"max_exposure_s", "alt_min_deg", "alt_max_deg", "filters"} {
		if _, ok := m[absent]; ok {
			t.Errorf("expected omitempty field %q absent, got %v", absent, m[absent])
		}
	}
	if _, ok := m["power"]; !ok {
		t.Fatal("power (no omitempty tag) must always be present, even at zero value")
	}

	maxExp := 120.0
	full := constraintsView{NodeID: "pier_01", Power: 0.8, MaxExposureS: &maxExp, Filters: []string{"L", "R"}}
	raw2, _ := json.Marshal(full)
	var m2 map[string]any
	_ = json.Unmarshal(raw2, &m2)
	if m2["max_exposure_s"] != 120.0 {
		t.Fatalf("max_exposure_s = %v, want 120", m2["max_exposure_s"])
	}
	filters, ok := m2["filters"].([]any)
	if !ok || len(filters) != 2 {
		t.Fatalf("filters = %v, want 2-entry array", m2["filters"])
	}
}

// TestProjectsViewJSONShape covers `saucepan projects --json`.
func TestProjectsViewJSONShape(t *testing.T) {
	v := projectsView{NodeID: "pier_01", Projects: []string{"camp-a", "camp-b"}}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["node_id"] != "pier_01" {
		t.Fatalf("node_id = %v, want pier_01", m["node_id"])
	}
	projects, ok := m["projects"].([]any)
	if !ok || len(projects) != 2 {
		t.Fatalf("projects = %v, want 2-entry array", m["projects"])
	}

	// Nil projects must still marshal as "null" or "[]", not be omitted —
	// there is no omitempty tag on Projects.
	empty := projectsView{NodeID: "pier_02"}
	raw2, _ := json.Marshal(empty)
	var m2 map[string]any
	_ = json.Unmarshal(raw2, &m2)
	if _, ok := m2["projects"]; !ok {
		t.Fatal("projects key must be present even when empty (no omitempty tag)")
	}
}

// TestBoardRowJSONShape covers `saucepan board --json` read mode.
func TestBoardRowJSONShape(t *testing.T) {
	now := time.Now().UTC()
	row := boardRow{NodeID: "pier_a", Message: "covering 10pm-2am", SentAt: now}
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["node_id"] != "pier_a" || m["message"] != "covering 10pm-2am" {
		t.Fatalf("row fields wrong: %v", m)
	}
	if _, ok := m["sent_at"]; !ok {
		t.Fatal("sent_at must be present")
	}
}

// TestPrintStatusTableRendersWaitingHint covers printStatusTable's
// human-readable rendering, including the "waiting" hint line that only
// prints for rows whose presence is exactly "waiting".
func TestPrintStatusTableRendersWaitingHint(t *testing.T) {
	rows := []statusRow{
		{NodeID: "pier_live", Presence: "live"},
		{NodeID: "pier_waiting", Presence: "waiting"},
	}
	out := captureStdout(t, func() { printStatusTable(rows, 5*time.Second) })
	if !strings.Contains(out, "pier_live") || !strings.Contains(out, "pier_waiting") {
		t.Fatalf("expected both node ids in table output: %q", out)
	}
	if !strings.Contains(out, "pier_waiting: no telemetry within") {
		t.Fatalf("expected the waiting hint for pier_waiting, got: %q", out)
	}
	if strings.Contains(out, "pier_live: no telemetry") {
		t.Fatalf("live row should not get a waiting hint: %q", out)
	}
}

// TestPrintConstraintsTableRendersDashesForNil covers the numOrDash
// integration path for a constraintsView with unset optional fields.
func TestPrintConstraintsTableRendersDashesForNil(t *testing.T) {
	v := constraintsView{NodeID: "pier_01", Power: 0.5, QualityTier: "standard"}
	out := captureStdout(t, func() { printConstraintsTable(v) })
	if !strings.Contains(out, dash) {
		t.Fatalf("expected dash placeholder for unset max_exposure_s, got: %q", out)
	}
}

// TestPrintBoardTableAgeFormatting covers the age-since-sent rendering,
// including the dash fallback for a zero SentAt.
func TestPrintBoardTableAgeFormatting(t *testing.T) {
	rows := []boardRow{
		{NodeID: "pier_a", Message: "hello", SentAt: time.Now().Add(-90 * time.Second)},
		{NodeID: "pier_b", Message: "no timestamp"}, // zero SentAt
	}
	out := captureStdout(t, func() { printBoardTable(rows) })
	if !strings.Contains(out, "ago") {
		t.Fatalf("expected an 'ago' age string for pier_a, got: %q", out)
	}
	if !strings.Contains(out, dash) {
		t.Fatalf("expected dash for pier_b's zero SentAt, got: %q", out)
	}
}

func TestUsesTLS(t *testing.T) {
	cases := []struct {
		broker string
		want   bool
	}{
		{"tcp://localhost:1883", false},
		{"ssl://broker.example.com:8883", true},
		{"mqtts://broker.example.com:8883", true},
		{"tls://broker.example.com:8883", true},
		{"SSL://UPPERCASE.example.com:8883", true},
		{"", false},
		{"ws://broker.example.com:9001", false},
	}
	for _, tc := range cases {
		if got := usesTLS(tc.broker); got != tc.want {
			t.Errorf("usesTLS(%q) = %v, want %v", tc.broker, got, tc.want)
		}
	}
}
