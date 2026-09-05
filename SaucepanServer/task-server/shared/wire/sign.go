package wire

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// MQTT command HMAC (#241). Env: MQTT_COMMAND_HMAC_SECRET (orchestrator + edge).
// Canonical string: "v1\n{type}\n{node_id}\n{sent_at}\n{payload_json}"

const mqttCommandHMACEnv = "MQTT_COMMAND_HMAC_SECRET"

// CommandMaxAge is the max age of sent_at for edge acceptance.
const CommandMaxAge = 5 * time.Minute

var (
	ErrCommandHMACSecretMissing = errors.New("MQTT_COMMAND_HMAC_SECRET unset")
	ErrCommandSignatureInvalid  = errors.New("MQTT command signature invalid")
	ErrCommandStale             = errors.New("MQTT command sent_at too old or missing")
	ErrCommandNodeMismatch      = errors.New("MQTT command node_id mismatch")
)

// MQTTCommandHMACSecret returns the shared command-signing secret (may be empty).
func MQTTCommandHMACSecret() string {
	return strings.TrimSpace(os.Getenv(mqttCommandHMACEnv))
}

// CommandCanonical builds the HMAC input for a command.
func CommandCanonical(cmdType, nodeID, sentAt string, payloadJSON []byte) string {
	return fmt.Sprintf("v1\n%s\n%s\n%s\n%s", cmdType, nodeID, sentAt, string(payloadJSON))
}

// SignCommandPayload returns hex(HMAC-SHA256) over the canonical string.
func SignCommandPayload(secret, cmdType, nodeID, sentAt string, payloadJSON []byte) (string, error) {
	if secret == "" {
		return "", ErrCommandHMACSecretMissing
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(CommandCanonical(cmdType, nodeID, sentAt, payloadJSON)))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// VerifyCommandSignature checks sig matches canonical bytes (constant-time).
func VerifyCommandSignature(secret, cmdType, nodeID, sentAt, sig string, payloadJSON []byte) error {
	if secret == "" {
		return ErrCommandHMACSecretMissing
	}
	sent, err := time.Parse(time.RFC3339, sentAt)
	if err != nil {
		return ErrCommandStale
	}
	now := time.Now().UTC()
	if sent.Before(now.Add(-CommandMaxAge)) || sent.After(now.Add(CommandMaxAge)) {
		return ErrCommandStale
	}
	want, err := SignCommandPayload(secret, cmdType, nodeID, sentAt, payloadJSON)
	if err != nil {
		return err
	}
	if !hmac.Equal([]byte(strings.ToLower(sig)), []byte(strings.ToLower(want))) {
		return ErrCommandSignatureInvalid
	}
	return nil
}

// SealCommand sets NodeID + SentAt + Sig on cmd using MQTT_COMMAND_HMAC_SECRET.
// Payload must be JSON-marshalable. Returns sealed command ready to marshal.
func SealCommand(cmdType, nodeID string, payload any) (Command, error) {
	secret := MQTTCommandHMACSecret()
	sentAt := time.Now().UTC().Format(time.RFC3339)
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return Command{}, err
	}
	cmd := Command{
		Type:    cmdType,
		NodeID:  nodeID,
		Payload: json.RawMessage(payloadJSON),
		SentAt:  sentAt,
	}
	if secret == "" {
		if os.Getenv("DEV_MODE") == "1" {
			return cmd, nil // unsigned only in DEV_MODE
		}
		return Command{}, ErrCommandHMACSecretMissing
	}
	sig, err := SignCommandPayload(secret, cmdType, nodeID, sentAt, payloadJSON)
	if err != nil {
		return Command{}, err
	}
	cmd.Sig = sig
	return cmd, nil
}

// NodeIDFromTopic extracts the last path segment after prefix, e.g.
// NodeIDFromTopic("/telemetry/abc", "/telemetry/") → "abc".
// Returns "" if topic does not match.
func NodeIDFromTopic(topic, prefix string) string {
	topic = strings.TrimSpace(topic)
	prefix = strings.TrimSuffix(prefix, "/") + "/"
	if !strings.HasPrefix(topic, prefix) {
		return ""
	}
	id := strings.TrimPrefix(topic, prefix)
	if id == "" || strings.Contains(id, "/") {
		return ""
	}
	return id
}
