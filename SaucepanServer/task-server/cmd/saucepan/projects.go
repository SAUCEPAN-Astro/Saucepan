package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/saucepan/hotpath/shared/wire"
)

// projectsView is the render shape for `saucepan projects` (§4).
type projectsView struct {
	NodeID   string   `json:"node_id"`
	Projects []string `json:"projects"`
}

func cmdProjects(args []string) int {
	fs := flag.NewFlagSet("projects", flag.ContinueOnError)
	g := bindGlobalFlags(fs)
	join := fs.String("join", "", "campaign id to opt in to")
	leave := fs.String("leave", "", "campaign id to opt out of")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	g.resolve()
	if g.node == "" {
		fmt.Fprintln(os.Stderr, "saucepan projects: --node is required")
		return exitError
	}

	client, err := connect(g.broker, g.timeout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "saucepan projects:", err)
		return exitError
	}
	defer client.Disconnect(250)

	// One code path for read and write, same as constraints (§4): with
	// neither --join nor --leave, applyProjects is a no-op.
	meta, err := modifyMetadata(client, g.node, g.timeout, func(m *wire.NodeMetadata) error {
		applyProjects(m, *join, *leave)
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "saucepan projects:", err)
		if err == errNoRetainedMetadata {
			return exitNoData
		}
		return exitError
	}

	view := projectsView{NodeID: g.node, Projects: meta.EnabledCampaignIDs}
	emit(g.json, view, func() {
		t := newTable()
		t.row("node_id", view.NodeID)
		t.row("projects", strings.Join(view.Projects, "  "))
		t.flush()
	})
	return exitOK
}

// applyProjects mutates m.EnabledCampaignIDs: join is idempotent, leave on
// an absent id is a no-op (§4). Standalone so tests exercise the exact
// production mutation path.
func applyProjects(m *wire.NodeMetadata, join, leave string) {
	if join != "" {
		found := false
		for _, id := range m.EnabledCampaignIDs {
			found = found || id == join
		}
		if !found {
			m.EnabledCampaignIDs = append(m.EnabledCampaignIDs, join)
		}
	}
	if leave != "" {
		out := m.EnabledCampaignIDs[:0:0]
		for _, id := range m.EnabledCampaignIDs {
			if id != leave {
				out = append(out, id)
			}
		}
		m.EnabledCampaignIDs = out
	}
}
