package shared

import "github.com/saucepan/hotpath/shared/wire"

// wire_alias.go is the single place to look for the shared/wire
// indirection described in docs/design/PIER_CLI.md §3. Every identifier
// below used to be declared directly in shared/*.go; it now lives in
// shared/wire, a stdlib-only package cmd/saucepan can import without
// pulling in zap/redis. Nothing outside shared/ should need to change.

// Type aliases — transparent, so every existing call site (struct literals,
// function signatures, field types) keeps compiling unchanged.
type (
	Telemetry          = wire.Telemetry
	NodeMetadata       = wire.NodeMetadata
	Command            = wire.Command
	AssignTaskPayload  = wire.AssignTaskPayload
	PreemptTaskPayload = wire.PreemptTaskPayload
	ObstructionMask    = wire.ObstructionMask
	MountLimits        = wire.MountLimits
	HorizonProfile     = wire.HorizonProfile
	BoardNote          = wire.BoardNote
	PierCodeRef        = wire.PierCodeRef
)

// Constants are re-declared, not aliased — Go has no const alias. These are
// untyped string constants, so a compile-time copy is exact.
const (
	TopicTelemetry     = wire.TopicTelemetry
	TopicCommands      = wire.TopicCommands
	TopicMetadata      = wire.TopicMetadata
	TopicStatus        = wire.TopicStatus
	TopicBoard         = wire.TopicBoard
	TopicCampaignBoard = wire.TopicCampaignBoard

	NodeStatusOnline  = wire.NodeStatusOnline
	NodeStatusOffline = wire.NodeStatusOffline
	NodeStatusBusy    = wire.NodeStatusBusy
	NodeStatusIdle    = wire.NodeStatusIdle

	CommandMaxAge = wire.CommandMaxAge
)

// Errors use var X = wire.X, never a re-declared errors.New — errors.Is
// compares identity, and a copy would silently break every caller that
// checks it.
var (
	ErrCommandHMACSecretMissing = wire.ErrCommandHMACSecretMissing
	ErrCommandSignatureInvalid  = wire.ErrCommandSignatureInvalid
	ErrCommandStale             = wire.ErrCommandStale
	ErrCommandNodeMismatch      = wire.ErrCommandNodeMismatch
)

// Functions get thin wrappers, not var X = wire.X — a wrapper keeps the
// identifier a function, keeps godoc attached, and matches the
// call-through-plug pattern used by the metrics/ consolidation.

// MQTTCommandHMACSecret returns the shared command-signing secret (may be empty).
func MQTTCommandHMACSecret() string {
	return wire.MQTTCommandHMACSecret()
}

// CommandCanonical builds the HMAC input for a command.
func CommandCanonical(cmdType, nodeID, sentAt string, payloadJSON []byte) string {
	return wire.CommandCanonical(cmdType, nodeID, sentAt, payloadJSON)
}

// SignCommandPayload returns hex(HMAC-SHA256) over the canonical string.
func SignCommandPayload(secret, cmdType, nodeID, sentAt string, payloadJSON []byte) (string, error) {
	return wire.SignCommandPayload(secret, cmdType, nodeID, sentAt, payloadJSON)
}

// VerifyCommandSignature checks sig matches canonical bytes (constant-time).
func VerifyCommandSignature(secret, cmdType, nodeID, sentAt, sig string, payloadJSON []byte) error {
	return wire.VerifyCommandSignature(secret, cmdType, nodeID, sentAt, sig, payloadJSON)
}

// SealCommand sets NodeID + SentAt + Sig on cmd using MQTT_COMMAND_HMAC_SECRET.
func SealCommand(cmdType, nodeID string, payload any) (Command, error) {
	return wire.SealCommand(cmdType, nodeID, payload)
}

// NodeIDFromTopic extracts the last path segment after prefix, e.g.
// NodeIDFromTopic("/telemetry/abc", "/telemetry/") → "abc".
func NodeIDFromTopic(topic, prefix string) string {
	return wire.NodeIDFromTopic(topic, prefix)
}

// SubscribeFilter turns a Topic* format string into its wildcard subscription
// filter ("%s" → "+"). See wire.SubscribeFilter.
func SubscribeFilter(topic string) string { return wire.SubscribeFilter(topic) }

// TopicPrefix is the fixed leading part of a Topic* format string, up to the
// first "%s". See wire.TopicPrefix.
func TopicPrefix(topic string) string { return wire.TopicPrefix(topic) }

// NewMessageID creates an opaque identifier for a board message.
func NewMessageID() string { return wire.NewMessageID() }

// SameBoardMessage filters retained replay/redelivery duplicates.
func SameBoardMessage(a, b BoardNote) bool { return wire.SameBoardMessage(a, b) }
