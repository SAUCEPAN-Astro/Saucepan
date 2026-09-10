package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/saucepan/hotpath/shared/wire"
)

// presence is the pure function from §5. Telemetry is never retained, so a
// telemetry message arriving during THIS window is proof of life no
// retained value can fake. /status/ is retained inconsistently across pier
// implementations (#459) and must never produce "offline" on its own —
// this function does not even take it as an argument.
func presence(telemetrySeen bool, window time.Duration) string {
	switch {
	case telemetrySeen:
		return "live"
	case window >= wire.TelemetryHeartbeatMax:
		return "offline"
	default:
		return "waiting"
	}
}

// statusRow is one line of `saucepan status` output, table and JSON (§4).
type statusRow struct {
	NodeID          string          `json:"node_id"`
	Presence        string          `json:"presence"`
	StatusRetained  string          `json:"status_retained,omitempty"`
	Telemetry       *wire.Telemetry `json:"telemetry,omitempty"`
	QualityTier     string          `json:"quality_tier,omitempty"`
	ObservedWindowS float64         `json:"observed_window_s"`
}

func cmdStatus(args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	g := bindGlobalFlags(fs)
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	g.resolve()

	client, err := connect(g.broker, g.timeout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "saucepan status:", err)
		return exitError
	}
	defer client.Disconnect(250)

	views := snapshot(client, g.node, g.timeout)
	if len(views) == 0 {
		fmt.Fprintln(os.Stderr, "saucepan status: no data for the requested node")
		return exitNoData
	}

	ids := make([]string, 0, len(views))
	for id := range views {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	rows := make([]statusRow, 0, len(ids))
	for _, id := range ids {
		v := views[id]
		row := statusRow{NodeID: id, Presence: presence(v.TelemetrySeen, g.timeout), Telemetry: v.Telemetry, ObservedWindowS: g.timeout.Seconds()}
		if v.StatusMsg != nil {
			row.StatusRetained = v.StatusMsg.Status
		}
		if v.Metadata != nil {
			row.QualityTier = v.Metadata.QualityTier
		}
		rows = append(rows, row)
	}

	emit(g.json, rows, func() { printStatusTable(rows, g.timeout) })
	return exitOK
}

func printStatusTable(rows []statusRow, window time.Duration) {
	t := newTable()
	t.row("NODE", "PRESENCE", "STATUS", "TASK", "ALT/AZ", "FILES", "LOAD", "TIER")
	for _, r := range rows {
		status, task, altaz, files, load := dash, dash, dash, dash, dash
		if tel := r.Telemetry; tel != nil {
			status, files, load = tel.Status, fmt.Sprintf("%d", tel.CompletedFiles), fmt.Sprintf("%.0f%%", tel.LoadPct)
			if tel.CurrentTaskID != nil {
				task = fmt.Sprintf("%d", *tel.CurrentTaskID)
			}
			if tel.MountAltDeg != nil && tel.MountAzDeg != nil {
				altaz = fmt.Sprintf("%.1f / %.1f", *tel.MountAltDeg, *tel.MountAzDeg)
			}
		}
		t.row(r.NodeID, r.Presence, status, task, altaz, files, load, r.QualityTier)
	}
	t.flush()
	for _, r := range rows {
		if r.Presence == "waiting" {
			fmt.Printf("\n%s: no telemetry within %s — re-run with --timeout 65s for a definitive result.\n", r.NodeID, window)
		}
	}
}
