// Package metrics — hot-path Observation envelope (metrics/contracts/observation.schema.json).
// Stays under task-server/internal/ — moving it out of this Go module's
// directory tree would break the github.com/saucepan/hotpath/internal/metrics
// import path without introducing a second go.mod + replace directive, which
// is a module-topology change, not a move (#426).
package metrics

import (
	"encoding/json"
	"fmt"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/google/uuid"
)

// Observation is the wire format for metric sidecar output and hot-path ops telemetry.
type Observation struct {
	SchemaVersion string                 `json:"schema_version"`
	ObservationID string                 `json:"observation_id"`
	EntityType    string                 `json:"entity_type"`
	EntityID      string                 `json:"entity_id"`
	Producer      string                 `json:"producer"`
	ObservedAt    string                 `json:"observed_at"`
	Metrics       map[string]interface{} `json:"metrics"`
	Context       map[string]interface{} `json:"context"`
	WaitPile      []string               `json:"wait_pile"`
}

// Hot-path ops fields (subset published on assign/preempt / telemetry forward).
type OpsMetrics struct {
	EvtType           string  `json:"ops.evt_type,omitempty"`
	EvtTS             string  `json:"ops.evt_ts,omitempty"`
	TaskID            int64   `json:"ops.task_id,omitempty"`
	NodeID            string  `json:"ops.node_id,omitempty"`
	OrchLatencyUs     int64   `json:"ops.orch_latency_us,omitempty"`
	MQTTPublishMs     float64 `json:"ops.mqtt_publish_latency_ms,omitempty"`
	MQTTTaskReceiveMs float64 `json:"ops.mqtt_task_receive_ms,omitempty"`
	NodeStatus        string  `json:"ops.node_status,omitempty"`
	SvcStatus         string  `json:"ops.svc_status,omitempty"`
	GradeDurationMs   float64 `json:"ops.grade_duration_ms,omitempty"`
}

// Publisher abstracts MQTT so tests can stub.
type Publisher interface {
	Publish(topic string, qos byte, retained bool, payload interface{}) mqtt.Token
}

// NewObservation builds a schema-compliant Observation envelope.
func NewObservation(producer, entityType, entityID string, metrics map[string]interface{}, context map[string]interface{}) Observation {
	if metrics == nil {
		metrics = map[string]interface{}{}
	}
	if context == nil {
		context = map[string]interface{}{}
	}
	return Observation{
		SchemaVersion: "1",
		ObservationID: uuid.NewString(),
		EntityType:    entityType,
		EntityID:      entityID,
		Producer:      producer,
		ObservedAt:    time.Now().UTC().Format(time.RFC3339Nano),
		Metrics:       metrics,
		Context:       context,
		WaitPile:      []string{},
	}
}

// PublishObservation publishes a hot-path ops observation to MQTT metrics topic.
// Topic: saucepan/nodes/{nodeID}/metrics (mqtt_decision_fields.yaml).
// Fail-open: returns error but callers must not block task push on failure.
func PublishObservation(client Publisher, nodeID string, obs Observation) error {
	if client == nil || nodeID == "" {
		return nil
	}
	topic := fmt.Sprintf("saucepan/nodes/%s/metrics", nodeID)
	payload, err := json.Marshal(obs)
	if err != nil {
		return err
	}
	token := client.Publish(topic, 1, false, payload)
	if !token.WaitTimeout(2 * time.Second) {
		return fmt.Errorf("metrics publish timeout topic=%s", topic)
	}
	return token.Error()
}
