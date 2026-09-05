// Command schedsim runs the offline scheduler KPI harness (#420).
//
//	go run ./cmd/schedsim -scenario baseline
//	go run ./cmd/schedsim -scenario duplicate400
//	go run ./cmd/schedsim -scenario duplicate400 -bug
//
// Paste FormatReport output into algorithm PR descriptions (gate for #406 / #421).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/saucepan/hotpath/shared/schedsim"
)

func main() {
	scenario := flag.String("scenario", "baseline", "baseline | duplicate400 | planned421")
	bug := flag.Bool("bug", false, "for duplicate400: enable #400 bug re-queue mode")
	lanes := flag.Bool("lanes", true, "for planned421: enable planned/interrupt lanes")
	asJSON := flag.Bool("json", false, "emit JSON report")
	flag.Parse()

	var r schedsim.Report
	switch *scenario {
	case "baseline":
		r = schedsim.RunBaseline()
	case "duplicate400":
		r = schedsim.DuplicateAssignScenario(*bug)
		if !*bug {
			if err := schedsim.AssertNoDuplicateAssign(r); err != nil {
				fmt.Fprintln(os.Stderr, err)
				if *asJSON {
					enc := json.NewEncoder(os.Stdout)
					enc.SetIndent("", "  ")
					_ = enc.Encode(r)
				} else {
					fmt.Print(schedsim.FormatReport(r))
				}
				os.Exit(1)
			}
		}
	case "planned421":
		r = schedsim.RunPlannedInterrupt(*lanes)
	default:
		fmt.Fprintf(os.Stderr, "unknown scenario %q\n", *scenario)
		os.Exit(2)
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(r); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	fmt.Print(schedsim.FormatReport(r))
}
