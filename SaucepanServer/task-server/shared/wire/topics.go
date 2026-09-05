// Package wire is the pier-facing MQTT contract: topic strings, payload
// types, and command signing. It imports only the standard library so that
// a monitoring-only binary (cmd/saucepan) can link the contract without
// pulling in server infrastructure (zap, redis, ...). See
// docs/design/PIER_CLI.md §3.
package wire

import "strings"

// Topic constants — moved from shared/models.go:204-207,227-230 (#457).
const (
	TopicTelemetry = "/telemetry/%s"
	TopicCommands  = "/commands/%s"
	TopicMetadata  = "/metadata/%s"
	TopicStatus    = "/status/%s"

	// TopicBoard is pier-to-pier, not pier-to-server: %s args are
	// (task_id, node_id), not a single node_id like the topics above.
	// Board membership is per-task, so the ACL grants it as a blanket
	// rule rather than the %u-pattern used for the topics above (#463).
	TopicBoard = "/board/%s/%s"

	// TopicCampaignBoard is the campaign-scoped board: %s args are
	// (campaign_id, node_id). Every pier working any task in a campaign
	// shares one board — the cross-task / handoff coordination case that
	// the per-task TopicBoard cannot reach (#470 step 8). Still
	// pier-to-pier, still under /board/#, so the existing blanket ACL
	// (`topic readwrite /board/#`) already covers it — do not widen.
	TopicCampaignBoard = "/board/campaign/%s/%s"

	NodeStatusOnline  = "online"
	NodeStatusOffline = "offline"
	NodeStatusBusy    = "busy"
	NodeStatusIdle    = "idle"
)

// SubscribeFilter turns a Topic* format string into its single-level-wildcard
// MQTT subscription filter: every "%s" segment becomes "+".
//
//	SubscribeFilter(TopicTelemetry)     // "/telemetry/+"
//	SubscribeFilter(TopicCampaignBoard) // "/board/campaign/+/+"
//
// Use this instead of hardcoding "/telemetry/+" etc. at a Subscribe call site
// (#451) so a rename in this file propagates instead of silently diverging.
func SubscribeFilter(topic string) string {
	return strings.ReplaceAll(topic, "%s", "+")
}

// TopicPrefix is the fixed leading part of a Topic* format string, up to and
// including the slash before the first "%s":
//
//	TopicPrefix(TopicTelemetry)     // "/telemetry/"
//	TopicPrefix(TopicCampaignBoard) // "/board/campaign/"
//
// Pair it with NodeIDFromTopic (or strings.TrimPrefix) when parsing an
// incoming topic, rather than repeating the literal prefix.
func TopicPrefix(topic string) string {
	if i := strings.Index(topic, "%s"); i >= 0 {
		return topic[:i]
	}
	return topic
}
