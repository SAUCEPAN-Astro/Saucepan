package main

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
)

// emit renders v as JSON when jsonMode is true, otherwise calls human.
// --json on every command is the entire extensibility story (§1): the tool
// composes with jq instead of growing flags.
func emit(jsonMode bool, v any, human func()) {
	if jsonMode {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(v); err != nil {
			fmt.Fprintln(os.Stderr, "saucepan: encode json:", err)
		}
		return
	}
	human()
}

// table is a thin text/tabwriter wrapper shared by every subcommand's
// human-readable rendering.
type table struct {
	w *tabwriter.Writer
}

func newTable() *table {
	return &table{w: tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)}
}

func (t *table) row(cols ...string) {
	for i, c := range cols {
		if i > 0 {
			fmt.Fprint(t.w, "\t")
		}
		fmt.Fprint(t.w, c)
	}
	fmt.Fprint(t.w, "\n")
}

func (t *table) flush() {
	t.w.Flush()
}

// dash renders "—" for anything not yet known — telemetry that hasn't
// arrived, a value the CLI never received.
const dash = "—"
