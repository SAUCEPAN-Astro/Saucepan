package main

import (
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/saucepan/hotpath/internal/pierjob"
	"github.com/saucepan/hotpath/shared/wire"
)

// boardWatch keeps a live, in-memory view of campaign messages, telescope
// metadata, and advisory presence. Researcher code receives the recent opaque
// message buffer and decides what any message means; this component never
// interprets message contents or publishes on its own.
//
// Three subscriptions feed it, all covered by the existing pier ACL:
//   - /board/campaign/+/+  → retained last message plus every live publish
//   - /status/+            → online/offline advisory presence
//   - /metadata/+          → retained telescope identity/location metadata
type boardWatch struct {
	mu       sync.RWMutex
	notes    map[string][]wire.BoardNote  // campaignID → bounded recent messages
	pending  map[string][]wire.BoardNote  // campaignID → messages not yet given to runner
	metadata map[string]wire.NodeMetadata // nodeID → retained metadata
	presence map[string]bool              // nodeID → online
}

const maxBoardMessagesPerCampaign = 512

func newBoardWatch() *boardWatch {
	return &boardWatch{
		notes:    map[string][]wire.BoardNote{},
		pending:  map[string][]wire.BoardNote{},
		metadata: map[string]wire.NodeMetadata{},
		presence: map[string]bool{},
	}
}

// campaignBoardFilter is the wildcard subscription covering every pier's note
// on every campaign board this pier can see.
var campaignBoardFilter = wire.SubscribeFilter(wire.TopicCampaignBoard)

// subscribe wires the board, presence, and metadata feeds on an
// already-connected client. A failure is returned so main can decide; an
// unsubscribed board watch yields empty snapshots, as before.
func (bw *boardWatch) subscribe(client mqtt.Client, timeout time.Duration) error {
	if token := client.Subscribe(campaignBoardFilter, 1, bw.onBoardMessage); token.WaitTimeout(timeout) && token.Error() != nil {
		return token.Error()
	}
	if token := client.Subscribe(wire.SubscribeFilter(wire.TopicStatus), 1, bw.onStatusMessage); token.WaitTimeout(timeout) && token.Error() != nil {
		return token.Error()
	}
	if token := client.Subscribe(wire.SubscribeFilter(wire.TopicMetadata), 1, bw.onMetadataMessage); token.WaitTimeout(timeout) && token.Error() != nil {
		return token.Error()
	}
	return nil
}

func (bw *boardWatch) onBoardMessage(_ mqtt.Client, msg mqtt.Message) {
	// topic: /board/campaign/{campaign_id}/{node_id}
	parts := strings.Split(strings.TrimPrefix(msg.Topic(), wire.TopicPrefix(wire.TopicCampaignBoard)), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return
	}
	campaignID, nodeID := parts[0], parts[1]

	bw.mu.Lock()
	defer bw.mu.Unlock()
	if len(msg.Payload()) == 0 {
		// Keep compatibility with the previous retained-clear operation.
		filtered := bw.notes[campaignID][:0]
		for _, note := range bw.notes[campaignID] {
			if note.NodeID != nodeID {
				filtered = append(filtered, note)
			}
		}
		bw.notes[campaignID] = filtered
		pending := bw.pending[campaignID][:0]
		for _, note := range bw.pending[campaignID] {
			if note.NodeID != nodeID {
				pending = append(pending, note)
			}
		}
		bw.pending[campaignID] = pending
		return
	}
	var note wire.BoardNote
	if json.Unmarshal(msg.Payload(), &note) != nil {
		return
	}
	// The topic identity is authoritative even if an older publisher omitted
	// or misstated the envelope's node_id.
	note.NodeID = nodeID
	if msg.Retained() && note.MessageID == "" {
		filtered := bw.notes[campaignID][:0]
		for _, previous := range bw.notes[campaignID] {
			if previous.NodeID != nodeID {
				filtered = append(filtered, previous)
			}
		}
		bw.notes[campaignID] = filtered
		pending := bw.pending[campaignID][:0]
		for _, previous := range bw.pending[campaignID] {
			if previous.NodeID != nodeID {
				pending = append(pending, previous)
			}
		}
		bw.pending[campaignID] = pending
	}
	for _, previous := range bw.notes[campaignID] {
		if wire.SameBoardMessage(previous, note) {
			return
		}
	}
	bw.notes[campaignID] = append(bw.notes[campaignID], note)
	if len(bw.notes[campaignID]) > maxBoardMessagesPerCampaign {
		bw.notes[campaignID] = bw.notes[campaignID][len(bw.notes[campaignID])-maxBoardMessagesPerCampaign:]
	}
	bw.pending[campaignID] = append(bw.pending[campaignID], note)
	if len(bw.pending[campaignID]) > maxBoardMessagesPerCampaign {
		bw.pending[campaignID] = bw.pending[campaignID][len(bw.pending[campaignID])-maxBoardMessagesPerCampaign:]
	}
}

func (bw *boardWatch) onStatusMessage(_ mqtt.Client, msg mqtt.Message) {
	var st wire.NodeStatus
	if json.Unmarshal(msg.Payload(), &st) != nil || st.NodeID == "" {
		return
	}
	bw.mu.Lock()
	bw.presence[st.NodeID] = st.Status == wire.NodeStatusOnline || st.Status == wire.NodeStatusIdle || st.Status == wire.NodeStatusBusy
	bw.mu.Unlock()
}

func (bw *boardWatch) onMetadataMessage(_ mqtt.Client, msg mqtt.Message) {
	var metadata wire.NodeMetadata
	if json.Unmarshal(msg.Payload(), &metadata) != nil || metadata.NodeID == "" {
		return
	}
	bw.mu.Lock()
	bw.metadata[metadata.NodeID] = metadata
	bw.mu.Unlock()
}

// boardSnapshot returns the bounded recent campaign messages in stable
// timestamp order. It is a signal buffer, not a semantic parser or dispatcher.
func (bw *boardWatch) boardSnapshot(campaignID string) []wire.BoardNote {
	if bw == nil {
		return nil
	}
	bw.mu.RLock()
	defer bw.mu.RUnlock()
	if len(bw.notes[campaignID]) == 0 {
		return nil
	}
	out := append([]wire.BoardNote(nil), bw.notes[campaignID]...)
	sortBoardMessages(out)
	return out
}

// drainBoardSnapshot returns each signal once to the next runner invocation.
// The recent history remains available for roster membership and diagnostics.
func (bw *boardWatch) drainBoardSnapshot(campaignID string) []wire.BoardNote {
	if bw == nil {
		return nil
	}
	bw.mu.Lock()
	defer bw.mu.Unlock()
	if len(bw.pending[campaignID]) == 0 {
		return nil
	}
	out := append([]wire.BoardNote(nil), bw.pending[campaignID]...)
	bw.pending[campaignID] = nil
	sortBoardMessages(out)
	return out
}

func sortBoardMessages(messages []wire.BoardNote) {
	sort.SliceStable(messages, func(i, j int) bool {
		if messages[i].SentAt.Equal(messages[j].SentAt) {
			if messages[i].NodeID == messages[j].NodeID {
				return messages[i].MessageID < messages[j].MessageID
			}
			return messages[i].NodeID < messages[j].NodeID
		}
		return messages[i].SentAt.Before(messages[j].SentAt)
	})
}

// pierRoster returns participating piers plus retained location metadata for
// researcher-side filtering. A node with no status message seen is reported
// offline (advisory, #459).
func (bw *boardWatch) pierRoster(campaignID, selfNodeID string) []pierjob.PierSummary {
	if bw == nil {
		return nil
	}
	bw.mu.RLock()
	defer bw.mu.RUnlock()
	seen := map[string]bool{}
	if selfNodeID != "" {
		seen[selfNodeID] = true
	}
	for _, note := range bw.notes[campaignID] {
		if note.NodeID != "" {
			seen[note.NodeID] = true
		}
	}
	for id, metadata := range bw.metadata {
		if containsString(metadata.EnabledCampaignIDs, campaignID) {
			seen[id] = true
		}
	}
	if len(seen) == 0 {
		return nil
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]pierjob.PierSummary, 0, len(ids))
	for _, id := range ids {
		online := bw.presence[id]
		if id == selfNodeID {
			online = true // we are, definitionally, online
		}
		metadata := bw.metadata[id]
		out = append(out, pierjob.PierSummary{
			NodeID:  id,
			Online:  online,
			SiteLat: metadata.SiteLat,
			SiteLon: metadata.SiteLon,
		})
	}
	return out
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
