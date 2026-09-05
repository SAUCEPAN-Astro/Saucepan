package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/saucepan/hotpath/shared/wire"
)

// boardRow is one line of `saucepan board` read-mode output, table and JSON.
type boardRow struct {
	NodeID  string    `json:"node_id"`
	Message string    `json:"message"`
	SentAt  time.Time `json:"sent_at"`
}

// boardScope addresses one board: either a single task or a whole campaign,
// never both. It owns the /board/ topic-template choice so postBoardNote and
// readBoard stay scope-agnostic.
type boardScope struct {
	kind string // "task" or "campaign"
	id   string
}

// topic renders the /board/ topic for one pier's note under this scope.
// nodeID == "+" gives the read-subscription filter; nodeID == "" gives the
// prefix NodeIDFromTopic expects.
func (s boardScope) topic(nodeID string) string {
	if s.kind == "campaign" {
		return fmt.Sprintf(wire.TopicCampaignBoard, s.id, nodeID)
	}
	return fmt.Sprintf(wire.TopicBoard, s.id, nodeID)
}

// stamp sets whichever of TaskID / CampaignID this scope addresses, leaving
// the other empty (it is omitempty on the wire).
func (s boardScope) stamp(n *wire.BoardNote) {
	if s.kind == "campaign" {
		n.CampaignID = s.id
	} else {
		n.TaskID = s.id
	}
}

// resolveBoardScope enforces exactly-one-of --task / --campaign.
func resolveBoardScope(task, campaign string) (boardScope, error) {
	switch {
	case task != "" && campaign != "":
		return boardScope{}, fmt.Errorf("--task and --campaign are mutually exclusive")
	case task != "":
		return boardScope{kind: "task", id: task}, nil
	case campaign != "":
		return boardScope{kind: "campaign", id: campaign}, nil
	default:
		return boardScope{}, fmt.Errorf("one of --task or --campaign is required")
	}
}

func cmdBoard(args []string) int {
	fs := flag.NewFlagSet("board", flag.ContinueOnError)
	g := bindGlobalFlags(fs)
	task := fs.String("task", "", "task id the board is scoped to")
	campaign := fs.String("campaign", "", "campaign id the board is scoped to (mutually exclusive with --task)")
	post := fs.String("post", "", "publish this arbitrary string as --node, then exit")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	g.resolve()

	scope, err := resolveBoardScope(*task, *campaign)
	if err != nil {
		fmt.Fprintln(os.Stderr, "saucepan board:", err)
		return exitError
	}

	client, err := connect(g.broker, g.timeout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "saucepan board:", err)
		return exitError
	}
	defer client.Disconnect(250)

	if *post != "" {
		if g.node == "" {
			fmt.Fprintln(os.Stderr, "saucepan board: --post requires --node (or SAUCEPAN_NODE_ID)")
			return exitError
		}
		note := wire.BoardNote{NodeID: g.node, MessageID: wire.NewMessageID(), Message: *post, SentAt: time.Now().UTC()}
		scope.stamp(&note)
		if err := postBoardNote(client, scope, note, g.timeout); err != nil {
			fmt.Fprintln(os.Stderr, "saucepan board:", err)
			return exitError
		}
		emit(g.json, note, func() {
			fmt.Printf("posted to %s %s as %s: %q\n", scope.kind, scope.id, g.node, *post)
		})
		return exitOK
	}

	rows := readBoard(client, scope, g.timeout)
	if len(rows) == 0 {
		fmt.Fprintf(os.Stderr, "saucepan board: no notes on this %s's board\n", scope.kind)
		return exitNoData
	}

	emit(g.json, rows, func() { printBoardTable(rows) })
	return exitOK
}

func printBoardTable(rows []boardRow) {
	t := newTable()
	t.row("NODE", "MESSAGE", "AGE")
	for _, r := range rows {
		age := dash
		if !r.SentAt.IsZero() {
			age = time.Since(r.SentAt).Round(time.Second).String() + " ago"
		}
		t.row(r.NodeID, r.Message, age)
	}
	t.flush()
}

// postBoardNote publishes an opaque message to the scope's retained topic.
// Active subscribers receive every publish; retention only seeds a late
// subscriber with the sender's latest message.
func postBoardNote(client mqtt.Client, scope boardScope, note wire.BoardNote, timeout time.Duration) error {
	payload, err := json.Marshal(note)
	if err != nil {
		return fmt.Errorf("marshal board note: %w", err)
	}
	topic := scope.topic(note.NodeID)
	token := client.Publish(topic, 1, true, payload)
	if !token.WaitTimeout(timeout) {
		return fmt.Errorf("publish %s: timed out after %s", topic, timeout)
	}
	return token.Error()
}

// readBoard subscribes to the scope's /board/.../+ topic for window and
// collects every signal received, including the retained latest message from
// each sender. Retention gives a late pier a starting point while active
// subscribers still receive independent publishes.
func readBoard(client mqtt.Client, scope boardScope, window time.Duration) []boardRow {
	topic := scope.topic("+")
	prefix := scope.topic("")

	notes := []wire.BoardNote{}
	var mu sync.Mutex
	client.Subscribe(topic, 1, func(_ mqtt.Client, msg mqtt.Message) {
		var note wire.BoardNote
		if json.Unmarshal(msg.Payload(), &note) != nil {
			return
		}
		id := wire.NodeIDFromTopic(msg.Topic(), prefix)
		if id == "" {
			return
		}
		note.NodeID = id
		mu.Lock()
		for _, previous := range notes {
			if wire.SameBoardMessage(previous, note) {
				mu.Unlock()
				return
			}
		}
		notes = append(notes, note)
		mu.Unlock()
	})
	time.Sleep(window)
	client.Unsubscribe(topic)

	mu.Lock()
	defer mu.Unlock()
	sort.SliceStable(notes, func(i, j int) bool {
		if notes[i].SentAt.Equal(notes[j].SentAt) {
			return notes[i].NodeID < notes[j].NodeID
		}
		return notes[i].SentAt.Before(notes[j].SentAt)
	})
	rows := make([]boardRow, 0, len(notes))
	for _, note := range notes {
		rows = append(rows, boardRow{NodeID: note.NodeID, Message: note.Message, SentAt: note.SentAt})
	}
	return rows
}
