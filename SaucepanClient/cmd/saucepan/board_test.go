package main

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/saucepan/hotpath/shared/wire"
)

func taskScope(id string) boardScope     { return boardScope{kind: "task", id: id} }
func campaignScope(id string) boardScope { return boardScope{kind: "campaign", id: id} }

func TestResolveBoardScope(t *testing.T) {
	tests := []struct {
		name         string
		task, camp   string
		wantKind, id string
		wantErr      bool
	}{
		{"task only", "task-1", "", "task", "task-1", false},
		{"campaign only", "", "camp-1", "campaign", "camp-1", false},
		{"both set is an error", "task-1", "camp-1", "", "", true},
		{"neither set is an error", "", "", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveBoardScope(tt.task, tt.camp)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("resolveBoardScope(%q,%q) = %+v, want error", tt.task, tt.camp, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveBoardScope(%q,%q): %v", tt.task, tt.camp, err)
			}
			if got.kind != tt.wantKind || got.id != tt.id {
				t.Fatalf("resolveBoardScope(%q,%q) = %+v, want {%s %s}", tt.task, tt.camp, got, tt.wantKind, tt.id)
			}
		})
	}
}

func TestPostBoardNotePublishesRetained(t *testing.T) {
	client := newFakeMQTTClient("", nil)
	note := wire.BoardNote{TaskID: "task-1", NodeID: "pier_a", Message: "covering 10pm-2am", SentAt: time.Now().UTC()}

	if err := postBoardNote(client, taskScope("task-1"), note, time.Second); err != nil {
		t.Fatalf("postBoardNote: %v", err)
	}

	topic := fmt.Sprintf(wire.TopicBoard, "task-1", "pier_a")
	published, ok := client.published[topic]
	if !ok {
		t.Fatalf("postBoardNote never published to %s", topic)
	}
	var got wire.BoardNote
	if err := json.Unmarshal(published, &got); err != nil {
		t.Fatalf("unmarshal published note: %v", err)
	}
	if !got.SentAt.Equal(note.SentAt) || got.TaskID != note.TaskID || got.NodeID != note.NodeID || got.Message != note.Message {
		t.Fatalf("published note = %+v, want %+v", got, note)
	}
}

// TestPostBoardNoteCampaignScopePublishesToCampaignTopic is the campaign
// mirror of the task post test: the note must land on
// /board/campaign/{id}/{node} and carry campaign_id, not task_id.
func TestPostBoardNoteCampaignScopePublishesToCampaignTopic(t *testing.T) {
	client := newFakeMQTTClient("", nil)
	note := wire.BoardNote{CampaignID: "camp-7", NodeID: "pier_a", Message: "M31 mosaic tile 3 done", SentAt: time.Now().UTC()}

	if err := postBoardNote(client, campaignScope("camp-7"), note, time.Second); err != nil {
		t.Fatalf("postBoardNote: %v", err)
	}

	topic := fmt.Sprintf(wire.TopicCampaignBoard, "camp-7", "pier_a")
	published, ok := client.published[topic]
	if !ok {
		t.Fatalf("postBoardNote never published to %s (published: %v)", topic, keysOf(client.published))
	}
	var got wire.BoardNote
	if err := json.Unmarshal(published, &got); err != nil {
		t.Fatalf("unmarshal published note: %v", err)
	}
	if got.CampaignID != "camp-7" || got.TaskID != "" {
		t.Fatalf("published note scoping = {task:%q campaign:%q}, want campaign-only", got.TaskID, got.CampaignID)
	}
}

// TestReadBoardCollectsEveryPierOnTheTask covers the cohort case
// (SelectCohort puts up to CohortMaxNodes on one task_id concurrently) and
// confirms a different task's board never leaks in — board topics are only
// scoped by task_id in the topic string, not by any broker-side isolation.
func TestReadBoardCollectsEveryPierOnTheTask(t *testing.T) {
	taskID := "task-1"
	notes := map[string]wire.BoardNote{
		"pier_a": {TaskID: taskID, NodeID: "pier_a", Message: "covering 10pm-2am", SentAt: time.Now().UTC()},
		"pier_b": {TaskID: taskID, NodeID: "pier_b", Message: "clouds, skipping tonight", SentAt: time.Now().UTC()},
	}

	client := newFakeMQTTClient("", nil)
	for nodeID, note := range notes {
		payload, err := json.Marshal(note)
		if err != nil {
			t.Fatalf("marshal seed note: %v", err)
		}
		client.seed(fmt.Sprintf(wire.TopicBoard, taskID, nodeID), payload)
	}
	other, err := json.Marshal(wire.BoardNote{TaskID: "task-2", NodeID: "pier_c", Message: "unrelated task's board"})
	if err != nil {
		t.Fatalf("marshal other-task note: %v", err)
	}
	client.seed(fmt.Sprintf(wire.TopicBoard, "task-2", "pier_c"), other)

	rows := readBoard(client, taskScope(taskID), 10*time.Millisecond)

	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2: %+v", len(rows), rows)
	}
	if rows[0].NodeID != "pier_a" || rows[1].NodeID != "pier_b" {
		t.Fatalf("rows not sorted by node id: %+v", rows)
	}
	if rows[0].Message != notes["pier_a"].Message || rows[1].Message != notes["pier_b"].Message {
		t.Fatalf("message mismatch: %+v", rows)
	}
}

// TestReadBoardCampaignScopeIsolatedFromOtherCampaignsAndTasks is the
// campaign mirror: a campaign board collects every pier that posted under
// that campaign_id, and neither another campaign's board nor a same-named
// task board bleeds in.
func TestReadBoardCampaignScopeIsolatedFromOtherCampaignsAndTasks(t *testing.T) {
	campID := "camp-1"
	notes := map[string]wire.BoardNote{
		"pier_a": {CampaignID: campID, NodeID: "pier_a", Message: "tile 1 of 4", SentAt: time.Now().UTC()},
		"pier_b": {CampaignID: campID, NodeID: "pier_b", Message: "tile 2 of 4", SentAt: time.Now().UTC()},
	}

	client := newFakeMQTTClient("", nil)
	for nodeID, note := range notes {
		payload, err := json.Marshal(note)
		if err != nil {
			t.Fatalf("marshal seed note: %v", err)
		}
		client.seed(fmt.Sprintf(wire.TopicCampaignBoard, campID, nodeID), payload)
	}
	otherCamp, _ := json.Marshal(wire.BoardNote{CampaignID: "camp-2", NodeID: "pier_x", Message: "different campaign"})
	client.seed(fmt.Sprintf(wire.TopicCampaignBoard, "camp-2", "pier_x"), otherCamp)
	taskNote, _ := json.Marshal(wire.BoardNote{TaskID: "camp-1", NodeID: "pier_y", Message: "task board, not campaign"})
	client.seed(fmt.Sprintf(wire.TopicBoard, "camp-1", "pier_y"), taskNote)

	rows := readBoard(client, campaignScope(campID), 10*time.Millisecond)

	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2: %+v", len(rows), rows)
	}
	if rows[0].NodeID != "pier_a" || rows[1].NodeID != "pier_b" {
		t.Fatalf("rows not sorted / wrong piers: %+v", rows)
	}
}

// TestReadBoardEmptyWhenNoNotesPosted is the exitNoData path cmdBoard uses
// when nobody has posted to a task's board yet.
func TestReadBoardEmptyWhenNoNotesPosted(t *testing.T) {
	client := newFakeMQTTClient("", nil)
	rows := readBoard(client, taskScope("task-with-no-notes"), 10*time.Millisecond)
	if len(rows) != 0 {
		t.Fatalf("got %d rows, want 0: %+v", len(rows), rows)
	}
}

// TestPostThenReadRoundTrips exercises both functions together against the
// same fake client, the way a second pier's `saucepan board --task X`
// would actually see what an earlier `--post` wrote.
func TestPostThenReadRoundTrips(t *testing.T) {
	client := newFakeMQTTClient("", nil)
	note := wire.BoardNote{TaskID: "task-9", NodeID: "pier_z", Message: "handing off at dawn", SentAt: time.Now().UTC()}

	if err := postBoardNote(client, taskScope("task-9"), note, time.Second); err != nil {
		t.Fatalf("postBoardNote: %v", err)
	}

	rows := readBoard(client, taskScope("task-9"), 10*time.Millisecond)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1: %+v", len(rows), rows)
	}
	if rows[0].NodeID != "pier_z" || rows[0].Message != "handing off at dawn" {
		t.Fatalf("row = %+v, want pier_z's note", rows[0])
	}
}

// TestPostThenReadRoundTripsCampaign is the campaign mirror of the
// post-then-read round trip.
func TestPostThenReadRoundTripsCampaign(t *testing.T) {
	client := newFakeMQTTClient("", nil)
	note := wire.BoardNote{CampaignID: "camp-9", NodeID: "pier_z", Message: "moving to tile 4", SentAt: time.Now().UTC()}

	if err := postBoardNote(client, campaignScope("camp-9"), note, time.Second); err != nil {
		t.Fatalf("postBoardNote: %v", err)
	}

	rows := readBoard(client, campaignScope("camp-9"), 10*time.Millisecond)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1: %+v", len(rows), rows)
	}
	if rows[0].NodeID != "pier_z" || rows[0].Message != "moving to tile 4" {
		t.Fatalf("row = %+v, want pier_z's note", rows[0])
	}
}

func keysOf(m map[string][]byte) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}
