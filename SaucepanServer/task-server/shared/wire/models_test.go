package wire

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestBoardNoteJSONRoundTrip covers both scopings: a task note carries
// task_id and omits campaign_id, a campaign note does the reverse. The
// omitempty tags keep the unused scope key off the wire entirely.
func TestBoardNoteJSONRoundTrip(t *testing.T) {
	now := time.Now().UTC().Round(time.Second)

	task := BoardNote{TaskID: "task-1", NodeID: "pier_a", Message: "covering 10pm-2am", SentAt: now}
	raw, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("marshal task note: %v", err)
	}
	if strings.Contains(string(raw), "campaign_id") {
		t.Fatalf("task note serialized a campaign_id key: %s", raw)
	}
	var backT BoardNote
	if err := json.Unmarshal(raw, &backT); err != nil {
		t.Fatalf("unmarshal task note: %v", err)
	}
	if backT.TaskID != task.TaskID || backT.CampaignID != "" || backT.NodeID != task.NodeID ||
		backT.Message != task.Message || !backT.SentAt.Equal(task.SentAt) {
		t.Fatalf("task round trip = %+v, want %+v", backT, task)
	}

	camp := BoardNote{CampaignID: "camp-7", NodeID: "pier_b", Message: "tile 3 of 4 done", SentAt: now}
	raw, err = json.Marshal(camp)
	if err != nil {
		t.Fatalf("marshal campaign note: %v", err)
	}
	if strings.Contains(string(raw), "task_id") {
		t.Fatalf("campaign note serialized a task_id key: %s", raw)
	}
	var backC BoardNote
	if err := json.Unmarshal(raw, &backC); err != nil {
		t.Fatalf("unmarshal campaign note: %v", err)
	}
	if backC.CampaignID != camp.CampaignID || backC.TaskID != "" || backC.NodeID != camp.NodeID ||
		backC.Message != camp.Message || !backC.SentAt.Equal(camp.SentAt) {
		t.Fatalf("campaign round trip = %+v, want %+v", backC, camp)
	}
}

func TestBoardMessageIdentityDistinguishesFastSignals(t *testing.T) {
	now := time.Now().UTC()
	a := BoardNote{NodeID: "pier_a", MessageID: "m1", Message: "Z9A7", SentAt: now}
	b := a
	b.MessageID = "m2"
	b.Message = "Q2B4"
	if SameBoardMessage(a, b) {
		t.Fatal("different message IDs must remain independent signals")
	}
	if !SameBoardMessage(a, a) {
		t.Fatal("same message ID must deduplicate retained replay")
	}
}

// TestTopicCampaignBoardFormatParses confirms the campaign board topic
// template composes and then decodes back to the node id with the shared
// NodeIDFromTopic helper — the same contract cmd/saucepan's readBoard
// relies on.
func TestTopicCampaignBoardFormatParses(t *testing.T) {
	topic := fmt.Sprintf(TopicCampaignBoard, "camp-7", "pier_a")
	if topic != "/board/campaign/camp-7/pier_a" {
		t.Fatalf("TopicCampaignBoard formatted to %q", topic)
	}

	prefix := fmt.Sprintf(TopicCampaignBoard, "camp-7", "")
	if got := NodeIDFromTopic(topic, prefix); got != "pier_a" {
		t.Fatalf("NodeIDFromTopic(%q, %q) = %q, want pier_a", topic, prefix, got)
	}

	// A task board topic must not parse under a campaign prefix.
	taskTopic := fmt.Sprintf(TopicBoard, "camp-7", "pier_a")
	if got := NodeIDFromTopic(taskTopic, prefix); got != "" {
		t.Fatalf("task topic %q parsed under campaign prefix %q as %q", taskTopic, prefix, got)
	}
}

// TestAssignTaskPayloadPierCodeRoundTrip covers #516: the grant map and kill
// switch ride the assign payload, omitempty keeps them off ordinary assigns.
func TestAssignTaskPayloadPierCodeRoundTrip(t *testing.T) {
	plain := AssignTaskPayload{TaskID: 1, Name: "m31"}
	b, _ := json.Marshal(plain)
	for _, k := range []string{"pier_code_grants", "pier_code_disabled", "pier_code"} {
		if strings.Contains(string(b), k) {
			t.Fatalf("ordinary assign leaked %q: %s", k, b)
		}
	}

	sum := sha256.Sum256([]byte("artifact"))
	withCode := AssignTaskPayload{
		TaskID:           2,
		PierCodeGrants:   map[string]bool{ActionReadFrame: true, ActionNextCapture: true},
		PierCode:         &PierCodeRef{SHA256: hex.EncodeToString(sum[:]), URL: "https://example.test/a.wasm"},
		PierCodeDisabled: true,
	}
	b, err := json.Marshal(withCode)
	if err != nil {
		t.Fatal(err)
	}
	var back AssignTaskPayload
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !back.PierCodeGrants[ActionNextCapture] || !back.PierCodeDisabled {
		t.Fatalf("round trip lost pier_code fields: %+v", back)
	}
	if back.PierCode == nil || back.PierCode.SHA256 != withCode.PierCode.SHA256 {
		t.Fatalf("round trip lost pier_code ref: %+v", back.PierCode)
	}
	if err := back.PierCode.Validate(); err != nil {
		t.Fatalf("round-tripped ref no longer valid: %v", err)
	}
}

func TestDefaultPierCodeGrantsIsReadAndBoardOnly(t *testing.T) {
	g := DefaultPierCodeGrants()
	for _, a := range []string{ActionReadFrame, ActionBoardPost, ActionBoardRead} {
		if !g[a] {
			t.Fatalf("default missing %s", a)
		}
	}
	for _, a := range []string{ActionInboxAlert, ActionUrgencyFlag, ActionListPiers, ActionRequestTime, ActionNextCapture} {
		if g[a] {
			t.Fatalf("default should not grant %s", a)
		}
	}
	if !IsPierCodeAction(ActionNextCapture) || IsPierCodeAction("bogus") {
		t.Fatal("IsPierCodeAction wrong")
	}
	if GrantAllows(nil, ActionReadFrame) || GrantAllows(map[string]bool{"bogus": true}, "bogus") {
		t.Fatal("GrantAllows must fail closed on nil map / unknown action")
	}
}
