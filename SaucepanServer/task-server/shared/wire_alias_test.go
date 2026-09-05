package shared

import (
	"testing"
	"time"
)

// wire_alias_test.go guards the shared → shared/wire call-through plugs
// (wire_alias.go). These are thin delegations; the real logic is tested in
// shared/wire. This just proves the plug itself forwards correctly and
// doesn't silently diverge from the wire package it aliases.

func TestWireAliasSignAndVerifyRoundTrip(t *testing.T) {
	secret := "alias-test-secret"
	payload := []byte(`{"task_id":9}`)
	sent := time.Now().UTC().Format(time.RFC3339)

	got := CommandCanonical("assign_task", "node_a", sent, payload)
	want := "v1\nassign_task\nnode_a\n" + sent + "\n" + string(payload)
	if got != want {
		t.Fatalf("CommandCanonical alias mismatch: got %q want %q", got, want)
	}

	sig, err := SignCommandPayload(secret, "assign_task", "node_a", sent, payload)
	if err != nil {
		t.Fatalf("SignCommandPayload alias: %v", err)
	}
	if err := VerifyCommandSignature(secret, "assign_task", "node_a", sent, sig, payload); err != nil {
		t.Fatalf("VerifyCommandSignature alias: %v", err)
	}
	if err := VerifyCommandSignature("wrong-secret", "assign_task", "node_a", sent, sig, payload); err == nil {
		t.Fatal("VerifyCommandSignature alias should reject wrong secret")
	}
}

func TestWireAliasSealCommand(t *testing.T) {
	t.Setenv("MQTT_COMMAND_HMAC_SECRET", "alias-seal-secret")
	cmd, err := SealCommand("assign_task", "n1", AssignTaskPayload{TaskID: 3})
	if err != nil {
		t.Fatalf("SealCommand alias: %v", err)
	}
	if cmd.NodeID != "n1" || cmd.Type != "assign_task" || cmd.Sig == "" {
		t.Fatalf("SealCommand alias incomplete: %+v", cmd)
	}
}

func TestWireAliasMQTTCommandHMACSecret(t *testing.T) {
	t.Setenv("MQTT_COMMAND_HMAC_SECRET", "  spaced  ")
	if got := MQTTCommandHMACSecret(); got != "spaced" {
		t.Fatalf("MQTTCommandHMACSecret alias: got %q want %q", got, "spaced")
	}
}

func TestWireAliasNodeIDFromTopic(t *testing.T) {
	if got := NodeIDFromTopic("/telemetry/pier_01", "/telemetry/"); got != "pier_01" {
		t.Fatalf("NodeIDFromTopic alias: got %q", got)
	}
	if got := NodeIDFromTopic("/telemetry/a/b", "/telemetry/"); got != "" {
		t.Fatalf("NodeIDFromTopic alias nested id should reject, got %q", got)
	}
}

func TestWireAliasTopicConstantsMatch(t *testing.T) {
	if TopicTelemetry != "/telemetry/%s" {
		t.Fatalf("TopicTelemetry alias diverged: %q", TopicTelemetry)
	}
	if TopicCommands != "/commands/%s" {
		t.Fatalf("TopicCommands alias diverged: %q", TopicCommands)
	}
	if TopicMetadata != "/metadata/%s" {
		t.Fatalf("TopicMetadata alias diverged: %q", TopicMetadata)
	}
	if TopicStatus != "/status/%s" {
		t.Fatalf("TopicStatus alias diverged: %q", TopicStatus)
	}
	if TopicBoard != "/board/%s/%s" {
		t.Fatalf("TopicBoard alias diverged: %q", TopicBoard)
	}
	if NodeStatusOnline != "online" || NodeStatusOffline != "offline" || NodeStatusBusy != "busy" || NodeStatusIdle != "idle" {
		t.Fatalf("NodeStatus* alias diverged: online=%q offline=%q busy=%q idle=%q",
			NodeStatusOnline, NodeStatusOffline, NodeStatusBusy, NodeStatusIdle)
	}
}

func TestWireAliasErrorIdentity(t *testing.T) {
	// errors.Is compares identity; these must be var X = wire.X, never a copy.
	if err := VerifyCommandSignature("", "t", "n", "s", "sig", nil); err != ErrCommandHMACSecretMissing {
		t.Fatalf("ErrCommandHMACSecretMissing identity broken: %v", err)
	}
}
