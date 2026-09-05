package wire

import (
	"crypto/hmac"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestSignAndVerifyCommand(t *testing.T) {
	secret := "test-secret-241"
	payload, _ := json.Marshal(map[string]any{"task_id": 7, "name": "m31"})
	sent := time.Now().UTC().Format(time.RFC3339)
	sig, err := SignCommandPayload(secret, "assign_task", "node_a", sent, payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyCommandSignature(secret, "assign_task", "node_a", sent, sig, payload); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if err := VerifyCommandSignature(secret, "assign_task", "node_b", sent, sig, payload); err == nil {
		t.Fatal("expected node mismatch to fail verify")
	}
	if err := VerifyCommandSignature("wrong", "assign_task", "node_a", sent, sig, payload); err == nil {
		t.Fatal("expected wrong secret to fail")
	}
	if !hmac.Equal([]byte(sig), []byte(sig)) {
		t.Fatal("sanity")
	}
}

func TestSealCommandRequiresSecretOutsideDev(t *testing.T) {
	t.Setenv(mqttCommandHMACEnv, "")
	t.Setenv("DEV_MODE", "")
	_, err := SealCommand("assign_task", "n1", AssignTaskPayload{TaskID: 1})
	if err != ErrCommandHMACSecretMissing {
		t.Fatalf("want ErrCommandHMACSecretMissing, got %v", err)
	}
	t.Setenv("DEV_MODE", "1")
	cmd, err := SealCommand("assign_task", "n1", AssignTaskPayload{TaskID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Sig != "" {
		t.Fatal("DEV unsigned seal should leave Sig empty")
	}
	t.Setenv("DEV_MODE", "")
	t.Setenv(mqttCommandHMACEnv, "prod-secret")
	cmd, err = SealCommand("assign_task", "n1", AssignTaskPayload{TaskID: 1, Name: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Sig == "" || cmd.NodeID != "n1" {
		t.Fatalf("sealed cmd incomplete: %+v", cmd)
	}
}

func TestNodeIDFromTopic(t *testing.T) {
	if got := NodeIDFromTopic("/telemetry/pier_01", "/telemetry/"); got != "pier_01" {
		t.Fatalf("got %q", got)
	}
	if got := NodeIDFromTopic("/telemetry/a/b", "/telemetry/"); got != "" {
		t.Fatalf("nested should reject, got %q", got)
	}
	if got := NodeIDFromTopic("/metadata/emu_x", "/metadata"); got != "emu_x" {
		t.Fatalf("got %q", got)
	}
}

func TestSealRoundTripEnv(t *testing.T) {
	os.Unsetenv(mqttCommandHMACEnv)
}

func TestCommandCanonical(t *testing.T) {
	got := CommandCanonical("assign_task", "node_a", "2026-01-01T00:00:00Z", []byte(`{"task_id":1}`))
	want := "v1\nassign_task\nnode_a\n2026-01-01T00:00:00Z\n{\"task_id\":1}"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	// Empty payload still produces a valid (parseable) canonical string.
	got = CommandCanonical("ping", "n1", "", nil)
	want = "v1\nping\nn1\n\n"
	if got != want {
		t.Fatalf("empty payload: got %q want %q", got, want)
	}
}

func TestVerifyCommandSignatureEmptySecret(t *testing.T) {
	if err := VerifyCommandSignature("", "assign_task", "node_a", "2026-01-01T00:00:00Z", "deadbeef", nil); err != ErrCommandHMACSecretMissing {
		t.Fatalf("want ErrCommandHMACSecretMissing, got %v", err)
	}
}

func TestVerifyCommandSignatureCaseInsensitive(t *testing.T) {
	secret := "test-secret-case"
	payload := []byte(`{"x":1}`)
	sent := time.Now().UTC().Format(time.RFC3339)
	sig, err := SignCommandPayload(secret, "ping", "node_a", sent, payload)
	if err != nil {
		t.Fatal(err)
	}
	upper := strings.ToUpper(sig)
	if err := VerifyCommandSignature(secret, "ping", "node_a", sent, upper, payload); err != nil {
		t.Fatalf("uppercase sig should still verify: %v", err)
	}
}

func TestVerifyCommandSignatureRejectsStaleSentAt(t *testing.T) {
	secret := "test-secret-stale"
	payload := []byte(`{"x":1}`)
	sent := time.Now().UTC().Add(-CommandMaxAge - time.Second).Format(time.RFC3339)
	sig, err := SignCommandPayload(secret, "ping", "node_a", sent, payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyCommandSignature(secret, "ping", "node_a", sent, sig, payload); err != ErrCommandStale {
		t.Fatalf("stale command error=%v, want ErrCommandStale", err)
	}
}

func TestVerifyCommandSignatureRejectsMalformedSentAt(t *testing.T) {
	secret := "test-secret-malformed-time"
	payload := []byte(`{"x":1}`)
	sig, err := SignCommandPayload(secret, "ping", "node_a", "not-a-time", payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyCommandSignature(secret, "ping", "node_a", "not-a-time", sig, payload); err != ErrCommandStale {
		t.Fatalf("malformed command error=%v, want ErrCommandStale", err)
	}
}

func TestVerifyCommandSignatureTamperedPayloadRejected(t *testing.T) {
	secret := "test-secret-tamper"
	sent := "2026-01-01T00:00:00Z"
	sig, err := SignCommandPayload(secret, "assign_task", "node_a", sent, []byte(`{"task_id":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyCommandSignature(secret, "assign_task", "node_a", sent, sig, []byte(`{"task_id":2}`)); err == nil {
		t.Fatal("expected tampered payload to fail verification")
	}
}

func TestVerifyCommandSignatureWrongSentAtRejected(t *testing.T) {
	secret := "test-secret-sentat"
	payload := []byte(`{"x":1}`)
	sig, err := SignCommandPayload(secret, "assign_task", "node_a", "2026-01-01T00:00:00Z", payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyCommandSignature(secret, "assign_task", "node_a", "2026-01-01T00:00:01Z", sig, payload); err == nil {
		t.Fatal("expected different sent_at to fail verification")
	}
}

func TestSignCommandPayloadEmptySecret(t *testing.T) {
	if _, err := SignCommandPayload("", "assign_task", "node_a", "2026-01-01T00:00:00Z", nil); err != ErrCommandHMACSecretMissing {
		t.Fatalf("want ErrCommandHMACSecretMissing, got %v", err)
	}
}

// unmarshalable is a payload type that always fails json.Marshal, to exercise
// SealCommand's marshal-error path.
type unmarshalable struct {
	Ch chan int
}

func TestSealCommandPayloadMarshalError(t *testing.T) {
	t.Setenv(mqttCommandHMACEnv, "some-secret")
	_, err := SealCommand("assign_task", "n1", unmarshalable{Ch: make(chan int)})
	if err == nil {
		t.Fatal("expected marshal error for unmarshalable payload")
	}
}

func TestSealCommandDifferentPayloadsDifferentSignatures(t *testing.T) {
	t.Setenv(mqttCommandHMACEnv, "seal-secret")
	c1, err := SealCommand("assign_task", "n1", AssignTaskPayload{TaskID: 1})
	if err != nil {
		t.Fatal(err)
	}
	c2, err := SealCommand("assign_task", "n1", AssignTaskPayload{TaskID: 2})
	if err != nil {
		t.Fatal(err)
	}
	if c1.Sig == c2.Sig {
		t.Fatal("different payloads must not produce the same signature")
	}
}

func TestMQTTCommandHMACSecretTrimsWhitespace(t *testing.T) {
	t.Setenv(mqttCommandHMACEnv, "  padded-secret  ")
	if got := MQTTCommandHMACSecret(); got != "padded-secret" {
		t.Fatalf("got %q want trimmed", got)
	}
	t.Setenv(mqttCommandHMACEnv, "")
	if got := MQTTCommandHMACSecret(); got != "" {
		t.Fatalf("empty env should yield empty secret, got %q", got)
	}
}

func TestNodeIDFromTopicEdgeCases(t *testing.T) {
	tests := []struct {
		name   string
		topic  string
		prefix string
		want   string
	}{
		{"empty topic", "", "/telemetry/", ""},
		{"empty prefix normalizes to a bare slash, no match", "abc", "", ""},
		{"exact prefix no id", "/telemetry/", "/telemetry/", ""},
		{"prefix without trailing slash still matches", "/metadata/emu_x", "/metadata", "emu_x"},
		{"prefix mismatch", "/status/n1", "/telemetry/", ""},
		{"trailing slash on topic yields empty segment after prefix", "/telemetry/n1/", "/telemetry/", ""},
		{"whitespace around topic trimmed", "  /telemetry/n1  ", "/telemetry/", "n1"},
		{"double slash prefix collapse", "/telemetry//n1", "/telemetry/", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NodeIDFromTopic(tt.topic, tt.prefix); got != tt.want {
				t.Fatalf("NodeIDFromTopic(%q, %q) = %q, want %q", tt.topic, tt.prefix, got, tt.want)
			}
		})
	}
}

func TestSealCommandDevModeUnsignedThenProdRequiresSecret(t *testing.T) {
	t.Setenv("DEV_MODE", "1")
	t.Setenv(mqttCommandHMACEnv, "")
	cmd, err := SealCommand("ping", "n1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Sig != "" || cmd.NodeID != "n1" || cmd.Type != "ping" {
		t.Fatalf("unexpected dev-mode command: %+v", cmd)
	}

	t.Setenv("DEV_MODE", "")
	_, err = SealCommand("ping", "n1", nil)
	if err != ErrCommandHMACSecretMissing {
		t.Fatalf("want ErrCommandHMACSecretMissing outside dev mode, got %v", err)
	}
}
