package main

import (
	"fmt"
	"math"

	"github.com/saucepan/hotpath/shared"
)

// bindTopicNodeID returns the authoritative node id from the MQTT topic.
// If JSON claims a different node_id, the message is rejected (#244).
func bindTopicNodeID(topic, prefix, jsonNodeID string) (nodeID string, ok bool) {
	topicID := shared.NodeIDFromTopic(topic, prefix)
	if topicID == "" {
		return "", false
	}
	if jsonNodeID != "" && jsonNodeID != topicID {
		return "", false
	}
	return topicID, true
}

// siteCoordJumpTooLarge rejects sudden site coordinate teleports in metadata (#244).
func siteCoordJumpTooLarge(prevLat, prevLon, nextLat, nextLon, maxDeg float64) bool {
	if maxDeg <= 0 {
		maxDeg = 5.0
	}
	dlat := (nextLat - prevLat) * math.Pi / 180
	dlon := (nextLon - prevLon) * math.Pi / 180
	lat1 := prevLat * math.Pi / 180
	lat2 := nextLat * math.Pi / 180
	a := math.Sin(dlat/2)*math.Sin(dlat/2) +
		math.Cos(lat1)*math.Cos(lat2)*math.Sin(dlon/2)*math.Sin(dlon/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	deg := c * 180 / math.Pi
	return deg > maxDeg
}

func formatReject(topic, reason string) string {
	return fmt.Sprintf("reject %s: %s", topic, reason)
}
