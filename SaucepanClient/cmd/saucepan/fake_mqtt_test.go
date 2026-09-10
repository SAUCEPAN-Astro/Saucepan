package main

import (
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// fakeMQTTClient is a minimal in-process stand-in for mqtt.Client so
// modifyMetadata and the board functions can be tested without a live
// broker. Subscribe delivers every preloaded "retained" payload whose topic
// matches (single-level "+" wildcard supported — that's all a real broker
// needs to match here, nothing needs "#"); topics with no match never call
// back, simulating no retained message.
type fakeMQTTClient struct {
	mu        sync.Mutex
	retained  map[string][]byte
	published map[string][]byte
}

func newFakeMQTTClient(retainedTopic string, retainedPayload []byte) *fakeMQTTClient {
	f := &fakeMQTTClient{retained: map[string][]byte{}, published: map[string][]byte{}}
	if retainedTopic != "" {
		f.retained[retainedTopic] = retainedPayload
	}
	return f
}

// seed adds another preloaded retained payload, for tests that need more
// than one topic populated (e.g. several piers' board notes on one task).
func (f *fakeMQTTClient) seed(topic string, payload []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.retained[topic] = payload
}

// topicMatches reports whether topic satisfies pattern, supporting the
// single-level "+" wildcard.
func topicMatches(pattern, topic string) bool {
	pSegs := strings.Split(pattern, "/")
	tSegs := strings.Split(topic, "/")
	if len(pSegs) != len(tSegs) {
		return false
	}
	for i, p := range pSegs {
		if p != "+" && p != tSegs[i] {
			return false
		}
	}
	return true
}

func (f *fakeMQTTClient) IsConnected() bool      { return true }
func (f *fakeMQTTClient) IsConnectionOpen() bool { return true }
func (f *fakeMQTTClient) Connect() mqtt.Token    { return &fakeToken{} }
func (f *fakeMQTTClient) Disconnect(uint)        {}

// Publish records every publish in f.published for assertions, and — when
// retain is true, matching real broker behavior — also updates f.retained
// so a later Subscribe on the same fake client sees its own prior write
// (needed for round-trip tests like post-then-read on one task's board).
func (f *fakeMQTTClient) Publish(topic string, _ byte, retain bool, payload interface{}) mqtt.Token {
	f.mu.Lock()
	defer f.mu.Unlock()
	var raw []byte
	switch p := payload.(type) {
	case []byte:
		raw = p
	case string:
		raw = []byte(p)
	default:
		return &fakeToken{}
	}
	f.published[topic] = raw
	if retain {
		f.retained[topic] = raw
	}
	return &fakeToken{}
}

func (f *fakeMQTTClient) Subscribe(topic string, qos byte, cb mqtt.MessageHandler) mqtt.Token {
	f.mu.Lock()
	matches := map[string][]byte{}
	for t, payload := range f.retained {
		if topicMatches(topic, t) {
			matches[t] = payload
		}
	}
	f.mu.Unlock()
	if cb != nil {
		for t, payload := range matches {
			cb(f, &fakeMessage{topic: t, payload: payload, retained: true, qos: qos})
		}
	}
	return &fakeToken{}
}

func (f *fakeMQTTClient) SubscribeMultiple(map[string]byte, mqtt.MessageHandler) mqtt.Token {
	return &fakeToken{}
}
func (f *fakeMQTTClient) Unsubscribe(...string) mqtt.Token        { return &fakeToken{} }
func (f *fakeMQTTClient) AddRoute(string, mqtt.MessageHandler)    {}
func (f *fakeMQTTClient) OptionsReader() mqtt.ClientOptionsReader { return mqtt.ClientOptionsReader{} }

// fakeToken is an already-completed, error-free mqtt.Token.
type fakeToken struct{}

func (*fakeToken) Wait() bool                     { return true }
func (*fakeToken) WaitTimeout(time.Duration) bool { return true }
func (*fakeToken) Done() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}
func (*fakeToken) Error() error { return nil }

// fakeMessage is a minimal mqtt.Message.
type fakeMessage struct {
	topic    string
	payload  []byte
	retained bool
	qos      byte
}

func (m *fakeMessage) Duplicate() bool   { return false }
func (m *fakeMessage) Qos() byte         { return m.qos }
func (m *fakeMessage) Retained() bool    { return m.retained }
func (m *fakeMessage) Topic() string     { return m.topic }
func (m *fakeMessage) MessageID() uint16 { return 0 }
func (m *fakeMessage) Payload() []byte   { return m.payload }
func (m *fakeMessage) Ack()              {}
