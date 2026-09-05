package main

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/saucepan/hotpath/shared/wire"
)

// connect opens a fresh, clean-session connection. Client id is
// saucepan-cli-<pid> so concurrent invocations never collide (§4).
// Credentials come from MQTT_USERNAME/MQTT_PASSWORD; the broker ACL keyed
// on username is the whole authorization story for a metadata write (§2).
func connect(broker string, timeout time.Duration) (mqtt.Client, error) {
	opts := mqtt.NewClientOptions().
		AddBroker(broker).
		SetClientID("saucepan-cli-" + strconv.Itoa(os.Getpid())).
		SetCleanSession(true).
		SetAutoReconnect(false).
		SetConnectTimeout(timeout)

	if user := strings.TrimSpace(os.Getenv("MQTT_USERNAME")); user != "" {
		opts.SetUsername(user)
		opts.SetPassword(os.Getenv("MQTT_PASSWORD"))
	}
	if usesTLS(broker) {
		// Scheme-based TLS, system roots only — no CA pinning, no insecure
		// escape hatch; a read-only monitor doesn't need that (§6).
		opts.SetTLSConfig(&tls.Config{MinVersion: tls.VersionTLS12})
	}

	client := mqtt.NewClient(opts)
	token := client.Connect()
	if !token.WaitTimeout(timeout) {
		return nil, fmt.Errorf("connect to %s: timed out after %s", broker, timeout)
	}
	if err := token.Error(); err != nil {
		return nil, fmt.Errorf("connect to %s: %w", broker, err)
	}
	return client, nil
}

func usesTLS(broker string) bool {
	b := strings.ToLower(broker)
	return strings.HasPrefix(b, "ssl://") || strings.HasPrefix(b, "mqtts://") || strings.HasPrefix(b, "tls://")
}

// nodeView is what the CLI observed about one node by the end of a window.
type nodeView struct {
	Metadata      *wire.NodeMetadata
	StatusMsg     *wire.NodeStatus
	Telemetry     *wire.Telemetry
	TelemetrySeen bool // true iff /telemetry/ arrived during THIS window — never from a retained value, there isn't one
}

// snapshot subscribes to /metadata/, /status/, /telemetry/ — wildcarded, or
// narrowed to nodeID — and collects the latest message per node for the
// full window: a hard deadline, so status can never hang (§4, §5).
func snapshot(client mqtt.Client, nodeID string, window time.Duration) map[string]*nodeView {
	views := map[string]*nodeView{}
	var mu sync.Mutex
	view := func(id string) *nodeView {
		mu.Lock()
		defer mu.Unlock()
		if views[id] == nil {
			views[id] = &nodeView{}
		}
		return views[id]
	}

	metaTopic := wire.SubscribeFilter(wire.TopicMetadata)
	statusTopic := wire.SubscribeFilter(wire.TopicStatus)
	telemetryTopic := wire.SubscribeFilter(wire.TopicTelemetry)
	metaPrefix := wire.TopicPrefix(wire.TopicMetadata)
	statusPrefix := wire.TopicPrefix(wire.TopicStatus)
	telemetryPrefix := wire.TopicPrefix(wire.TopicTelemetry)
	if nodeID != "" {
		metaTopic = fmt.Sprintf(wire.TopicMetadata, nodeID)
		statusTopic = fmt.Sprintf(wire.TopicStatus, nodeID)
		telemetryTopic = fmt.Sprintf(wire.TopicTelemetry, nodeID)
	}

	sub := func(topic, prefix string, qos byte, handle func(id string, payload []byte)) {
		client.Subscribe(topic, qos, func(_ mqtt.Client, msg mqtt.Message) {
			if id := wire.NodeIDFromTopic(msg.Topic(), prefix); id != "" {
				handle(id, msg.Payload())
			}
		})
	}
	sub(metaTopic, metaPrefix, 1, func(id string, payload []byte) {
		var m wire.NodeMetadata
		if json.Unmarshal(payload, &m) == nil {
			view(id).Metadata = &m
		}
	})
	sub(statusTopic, statusPrefix, 1, func(id string, payload []byte) {
		var s wire.NodeStatus
		if json.Unmarshal(payload, &s) == nil {
			view(id).StatusMsg = &s
		}
	})
	sub(telemetryTopic, telemetryPrefix, 0, func(id string, payload []byte) {
		var t wire.Telemetry
		if json.Unmarshal(payload, &t) == nil {
			v := view(id)
			v.Telemetry, v.TelemetrySeen = &t, true
		}
	})

	time.Sleep(window)
	client.Unsubscribe(metaTopic, statusTopic, telemetryTopic)

	mu.Lock()
	defer mu.Unlock()
	return views
}

// errNoRetainedMetadata: refusing to write is the fail-closed posture §4
// requires — there is nothing to modify, so publishing a fresh struct
// would zero every field the pier ever set.
var errNoRetainedMetadata = errors.New("no retained metadata within timeout — refusing to write")

// modifyMetadata is the mandatory read-modify-write from §4: subscribe
// /metadata/{node_id} and wait for the retained message; if none arrives
// within timeout, fail closed; otherwise unmarshal, let mutate change only
// the fields it owns, and re-publish QoS 1 retained. Round-tripping the
// full struct is what preserves every field the CLI doesn't know about,
// including both anomaly fields.
func modifyMetadata(client mqtt.Client, nodeID string, timeout time.Duration, mutate func(*wire.NodeMetadata) error) (*wire.NodeMetadata, error) {
	topic := fmt.Sprintf(wire.TopicMetadata, nodeID)

	received := make(chan []byte, 1)
	client.Subscribe(topic, 1, func(_ mqtt.Client, msg mqtt.Message) {
		select {
		case received <- msg.Payload():
		default:
		}
	})
	defer client.Unsubscribe(topic)

	var payload []byte
	select {
	case payload = <-received:
	case <-time.After(timeout):
		return nil, errNoRetainedMetadata
	}

	var meta wire.NodeMetadata
	if err := json.Unmarshal(payload, &meta); err != nil {
		return nil, fmt.Errorf("unmarshal retained metadata: %w", err)
	}
	if err := mutate(&meta); err != nil {
		return nil, err
	}

	out, err := json.Marshal(meta)
	if err != nil {
		return nil, fmt.Errorf("marshal metadata: %w", err)
	}
	pubToken := client.Publish(topic, 1, true, out)
	if !pubToken.WaitTimeout(timeout) {
		return nil, fmt.Errorf("publish %s: timed out after %s", topic, timeout)
	}
	if err := pubToken.Error(); err != nil {
		return nil, fmt.Errorf("publish %s: %w", topic, err)
	}
	return &meta, nil
}
