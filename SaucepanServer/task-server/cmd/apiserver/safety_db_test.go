package main

import (
	"encoding/json"
	"testing"
)

func TestParseObstructionJSONEmptyRaw(t *testing.T) {
	mask, err := parseObstructionJSON(nil)
	if err != nil || mask != nil {
		t.Fatalf("parseObstructionJSON(nil) = (%v, %v), want (nil, nil)", mask, err)
	}
	mask, err = parseObstructionJSON(json.RawMessage{})
	if err != nil || mask != nil {
		t.Fatalf("parseObstructionJSON(empty) = (%v, %v), want (nil, nil)", mask, err)
	}
}

func TestParseObstructionJSONMalformed(t *testing.T) {
	_, err := parseObstructionJSON(json.RawMessage(`not json`))
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestParseObstructionJSONValid(t *testing.T) {
	raw := json.RawMessage(`[[[10,20],[30,40],[50,60]]]`)
	mask, err := parseObstructionJSON(raw)
	if err != nil {
		t.Fatalf("parseObstructionJSON valid: %v", err)
	}
	if len(mask) != 1 || len(mask[0]) != 3 {
		t.Fatalf("unexpected mask shape: %+v", mask)
	}
}

func TestParseObstructionJSONFailsValidation(t *testing.T) {
	// Only 2 vertices — ValidateObstructionMask requires >= 3.
	raw := json.RawMessage(`[[[10,20],[30,40]]]`)
	_, err := parseObstructionJSON(raw)
	if err == nil {
		t.Fatal("expected validation error for polygon with < 3 vertices")
	}
}

func TestParseObstructionJSONOutOfRangeAltitude(t *testing.T) {
	raw := json.RawMessage(`[[[999,20],[30,40],[50,60]]]`)
	_, err := parseObstructionJSON(raw)
	if err == nil {
		t.Fatal("expected validation error for out-of-range altitude")
	}
}

func TestLiveObstructionMaskNoSnapshot(t *testing.T) {
	telemetryMu.Lock()
	delete(telemetryCache, "no-such-telescope")
	telemetryMu.Unlock()

	if mask, err := liveObstructionMask("no-such-telescope"); err != nil || mask != nil {
		t.Fatalf("expected nil mask for missing snapshot, got %v", mask)
	}
}

func TestLiveObstructionMaskEmptyMaskBytes(t *testing.T) {
	id := "telescope-empty-mask"
	telemetryMu.Lock()
	telemetryCache[id] = telemetrySnapshot{TelescopeID: id}
	telemetryMu.Unlock()
	t.Cleanup(func() {
		telemetryMu.Lock()
		delete(telemetryCache, id)
		telemetryMu.Unlock()
	})

	if mask, err := liveObstructionMask(id); err != nil || mask != nil {
		t.Fatalf("expected nil mask for snapshot with no live mask bytes, got %v", mask)
	}
}

func TestLiveObstructionMaskParsesSnapshotBytes(t *testing.T) {
	id := "telescope-with-mask"
	telemetryMu.Lock()
	telemetryCache[id] = telemetrySnapshot{
		TelescopeID:         id,
		ObstructionMaskLive: []byte(`[[[10,20],[30,40],[50,60]]]`),
	}
	telemetryMu.Unlock()
	t.Cleanup(func() {
		telemetryMu.Lock()
		delete(telemetryCache, id)
		telemetryMu.Unlock()
	})

	mask, err := liveObstructionMask(id)
	if err != nil {
		t.Fatalf("parse live mask: %v", err)
	}
	if len(mask) != 1 || len(mask[0]) != 3 {
		t.Fatalf("expected parsed mask, got %+v", mask)
	}
}

func TestLiveObstructionMaskRejectsMalformedSnapshot(t *testing.T) {
	id := "telescope-malformed-mask"
	telemetryMu.Lock()
	telemetryCache[id] = telemetrySnapshot{TelescopeID: id, ObstructionMaskLive: []byte("not-json")}
	telemetryMu.Unlock()
	t.Cleanup(func() {
		telemetryMu.Lock()
		delete(telemetryCache, id)
		telemetryMu.Unlock()
	})

	if _, err := liveObstructionMask(id); err == nil {
		t.Fatal("malformed live obstruction mask must be rejected")
	}
}
