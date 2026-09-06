package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/saucepan/hotpath/shared/wire"
)

// bwMsg is a minimal mqtt.Message: the boardWatch handlers only read Topic()
// and Payload().
type bwMsg struct {
	topic   string
	payload []byte
}

func (m bwMsg) Duplicate() bool   { return false }
func (m bwMsg) Qos() byte         { return 1 }
func (m bwMsg) Retained() bool    { return true }
func (m bwMsg) Topic() string     { return m.topic }
func (m bwMsg) MessageID() uint16 { return 0 }
func (m bwMsg) Payload() []byte   { return m.payload }
func (m bwMsg) Ack()              {}

func boardMsg(t *testing.T, campaignID, nodeID string, note wire.BoardNote) bwMsg {
	t.Helper()
	b, err := json.Marshal(note)
	if err != nil {
		t.Fatal(err)
	}
	return bwMsg{topic: "/board/campaign/" + campaignID + "/" + nodeID, payload: b}
}

func TestBoardWatchCollectsNotesAndDerivesRoster(t *testing.T) {
	bw := newBoardWatch()

	bw.onBoardMessage(nil, boardMsg(t, "camp-1", "pier_a", wire.BoardNote{NodeID: "pier_a", Message: "clear", SentAt: time.Now()}))
	bw.onBoardMessage(nil, boardMsg(t, "camp-1", "pier_b", wire.BoardNote{NodeID: "pier_b", Message: "cloud", SentAt: time.Now()}))
	bw.onBoardMessage(nil, boardMsg(t, "camp-2", "pier_c", wire.BoardNote{NodeID: "pier_c", Message: "other", SentAt: time.Now()}))

	st, _ := json.Marshal(wire.NodeStatus{NodeID: "pier_b", Status: wire.NodeStatusOnline})
	bw.onStatusMessage(nil, bwMsg{topic: "/status/pier_b", payload: st})

	notes := bw.boardSnapshot("camp-1")
	if len(notes) != 2 || notes[0].NodeID != "pier_a" || notes[1].NodeID != "pier_b" {
		t.Fatalf("camp-1 snapshot = %+v", notes)
	}
	if bw.boardSnapshot("camp-2")[0].Message != "other" {
		t.Fatalf("camp-2 snapshot wrong")
	}

	roster := bw.pierRoster("camp-1", "pier_self")
	// pier_self + pier_a + pier_b, sorted
	if len(roster) != 3 {
		t.Fatalf("roster = %+v", roster)
	}
	got := map[string]bool{}
	for _, p := range roster {
		got[p.NodeID] = p.Online
	}
	if !got["pier_self"] {
		t.Errorf("self must be online")
	}
	if !got["pier_b"] {
		t.Errorf("pier_b posted a status online, should be online")
	}
	if got["pier_a"] {
		t.Errorf("pier_a had no status message, should be offline (advisory)")
	}
}

func TestBoardWatchRetainedClearDropsNote(t *testing.T) {
	bw := newBoardWatch()
	bw.onBoardMessage(nil, boardMsg(t, "c", "p", wire.BoardNote{NodeID: "p", Message: "hi"}))
	if len(bw.boardSnapshot("c")) != 1 {
		t.Fatal("note not recorded")
	}
	bw.onBoardMessage(nil, bwMsg{topic: "/board/campaign/c/p", payload: nil})
	if len(bw.boardSnapshot("c")) != 0 {
		t.Fatal("retained-clear should drop the note")
	}
}

func TestBoardWatchKeepsIndependentMessagesFromOnePier(t *testing.T) {
	bw := newBoardWatch()
	bw.onBoardMessage(nil, boardMsg(t, "c", "p", wire.BoardNote{NodeID: "p", MessageID: "m1", Message: "first"}))
	bw.onBoardMessage(nil, boardMsg(t, "c", "p", wire.BoardNote{NodeID: "p", MessageID: "m2", Message: "replacement"}))

	notes := bw.boardSnapshot("c")
	if len(notes) != 2 || notes[0].Message != "first" || notes[1].Message != "replacement" {
		t.Fatalf("board signal snapshot = %+v, want both messages", notes)
	}
}

func TestBoardWatchDeduplicatesRetainedReplay(t *testing.T) {
	bw := newBoardWatch()
	note := wire.BoardNote{NodeID: "p", MessageID: "m1", Message: "same"}
	bw.onBoardMessage(nil, boardMsg(t, "c", "p", note))
	bw.onBoardMessage(nil, boardMsg(t, "c", "p", note))
	if notes := bw.boardSnapshot("c"); len(notes) != 1 {
		t.Fatalf("duplicate retained replay produced %d messages", len(notes))
	}
}

func TestBoardWatchDrainsSignalsOnce(t *testing.T) {
	bw := newBoardWatch()
	bw.onBoardMessage(nil, boardMsg(t, "c", "p", wire.BoardNote{NodeID: "p", MessageID: "m1", Message: "Z9A7"}))
	if got := bw.drainBoardSnapshot("c"); len(got) != 1 || got[0].Message != "Z9A7" {
		t.Fatalf("first drain = %+v", got)
	}
	if got := bw.drainBoardSnapshot("c"); len(got) != 0 {
		t.Fatalf("second drain = %+v, want empty", got)
	}
	if got := bw.boardSnapshot("c"); len(got) != 1 {
		t.Fatalf("recent history lost after drain: %+v", got)
	}
}

func TestBoardWatchCarriesLocationMetadata(t *testing.T) {
	bw := newBoardWatch()
	lat, lon := 28.6, 77.2
	metadata := wire.NodeMetadata{NodeID: "peer", SiteLat: &lat, SiteLon: &lon, EnabledCampaignIDs: []string{"c"}}
	raw, _ := json.Marshal(metadata)
	bw.onMetadataMessage(nil, bwMsg{topic: "/metadata/peer", payload: raw})

	roster := bw.pierRoster("c", "self")
	if len(roster) != 2 || roster[0].NodeID != "peer" || roster[0].SiteLat == nil || *roster[0].SiteLat != lat || roster[0].SiteLon == nil || *roster[0].SiteLon != lon {
		t.Fatalf("location-aware roster = %+v", roster)
	}
}

func TestBoardWatchStatusIsAdvisory(t *testing.T) {
	bw := newBoardWatch()
	bw.onBoardMessage(nil, boardMsg(t, "c", "peer", wire.BoardNote{NodeID: "peer", Message: "hello"}))

	status, _ := json.Marshal(wire.NodeStatus{NodeID: "peer", Status: wire.NodeStatusOffline})
	bw.onStatusMessage(nil, bwMsg{topic: "/status/peer", payload: status})
	if roster := bw.pierRoster("c", "self"); len(roster) != 2 || roster[0].Online || !roster[1].Online {
		t.Fatalf("offline advisory roster = %+v, want peer and self entries with peer offline", roster)
	}

	// A status message is not a liveness proof; the board watch has no
	// telemetry signal and therefore reports the peer as not online here.
	online, _ := json.Marshal(wire.NodeStatus{NodeID: "peer", Status: wire.NodeStatusOnline})
	bw.onStatusMessage(nil, bwMsg{topic: "/status/peer", payload: online})
	if roster := bw.pierRoster("c", "self"); len(roster) != 2 || !roster[0].Online {
		t.Fatalf("online advisory roster = %+v, want peer online", roster)
	}
}

func TestBoardWatchNilSafe(t *testing.T) {
	var bw *boardWatch
	if bw.boardSnapshot("x") != nil || bw.pierRoster("x", "y") != nil {
		t.Fatal("nil boardWatch must yield nil snapshots")
	}
}
