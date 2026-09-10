package main

import (
	"testing"
	"time"
)

// TestConnectRefusedFailsFast covers connect()'s error path against a
// broker that actively refuses the connection (nothing listens on
// 127.0.0.1:1 — a privileged, essentially never-bound port). No live MQTT
// broker is available in this test environment (see task instructions), so
// this is the one connect() case that's both real-network and fast/
// deterministic: a TCP RST arrives immediately, well under the timeout.
func TestConnectRefusedFailsFast(t *testing.T) {
	start := time.Now()
	client, err := connect("tcp://127.0.0.1:1", 2*time.Second)
	elapsed := time.Since(start)

	if err == nil {
		if client != nil {
			client.Disconnect(0)
		}
		t.Fatal("expected an error connecting to a refused port, got nil")
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("connect() took the full timeout (%s) for a refused connection; expected a fast TCP-level failure", elapsed)
	}
}
