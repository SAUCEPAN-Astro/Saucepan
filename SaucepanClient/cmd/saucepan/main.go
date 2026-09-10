// Command saucepan is the single Saucepan pier client. Its `run` mode is the
// resident BOINC-style client that connects hardware, receives signed
// assignments, captures safely, and publishes telemetry. Its other commands
// are one-shot monitoring and operator controls for the same pier.
//
// One deliberate piece of local state: `saucepan consent` reads and writes
// a small JSON file recording which campaigns' on-pier code (#470) the
// operator has approved for this machine. Every other command is stateless.
// See docs/design/PIER_CLI.md.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	pieragent "github.com/saucepan/saucepan-client/internal/pieragent"
)

// Exit codes (PIER_CLI.md §4): 0 ok, 1 error, 2 no data for the requested node.
const (
	exitOK     = 0
	exitError  = 1
	exitNoData = 2
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage(os.Stderr)
		return exitError
	}
	switch cmd, rest := args[0], args[1:]; cmd {
	case "run":
		return cmdRun(rest)
	case "status":
		return cmdStatus(rest)
	case "constraints":
		return cmdConstraints(rest)
	case "projects":
		return cmdProjects(rest)
	case "board":
		return cmdBoard(rest)
	case "consent":
		return cmdConsent(rest)
	case "-h", "--help", "help":
		usage(os.Stdout)
		return exitOK
	default:
		fmt.Fprintf(os.Stderr, "saucepan: unknown command %q\n\n", cmd)
		usage(os.Stderr)
		return exitError
	}
}

func usage(w *os.File) {
	fmt.Fprint(w, `saucepan — the Saucepan pier client

Usage:
	  saucepan run          start the resident client (hardware + monitoring)
  saucepan status      [--json] [--broker <url>] [--node <id>] [--timeout <dur>]
  saucepan constraints --node <id> [--json] [--power P] [--max-exposure S]
                         [--alt-min D] [--alt-max D] [--filters L,R,G,B]
  saucepan projects    --node <id> [--json] [--join <id>] [--leave <id>]
  saucepan board       (--task <id> | --campaign <id>) [--json] [--node <id>] [--post "<message>"]
  saucepan consent     [--list] [--approve <campaign_id> [--grants a,b,c]] [--revoke <campaign_id>] [--json]

Global flags: --json  --broker <url> (env MQTT_BROKER)  --node <id> (env SAUCEPAN_NODE_ID)  --timeout <dur> (default 5s)
consent is local-only (no broker): the per-campaign approval a pier operator must give before on-pier researcher code (#470) runs on this machine.
Env: MQTT_BROKER, MQTT_USERNAME, MQTT_PASSWORD, SAUCEPAN_NODE_ID
Exit codes: 0 ok, 1 error, 2 no data for the requested node.
`)
}

// cmdRun starts the resident pier client. Keeping this as a subcommand makes
// accidental hardware startup impossible when an operator only wants help.
func cmdRun(args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(os.Stderr, "saucepan run: no arguments are accepted")
		return exitError
	}
	if err := pieragent.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "saucepan run:", err)
		return exitError
	}
	return exitOK
}

// globalFlags is the set of flags common to every subcommand (§4).
type globalFlags struct {
	json    bool
	broker  string
	node    string
	timeout time.Duration
}

func bindGlobalFlags(fs *flag.FlagSet) *globalFlags {
	g := &globalFlags{}
	fs.BoolVar(&g.json, "json", false, "emit JSON")
	fs.StringVar(&g.broker, "broker", "", "MQTT broker URL (env MQTT_BROKER)")
	fs.StringVar(&g.node, "node", "", "target node id (env SAUCEPAN_NODE_ID)")
	fs.DurationVar(&g.timeout, "timeout", 5*time.Second, "listen / retained-read deadline")
	return g
}

// resolve applies environment fallbacks. Flags win when both are set (§4).
func (g *globalFlags) resolve() {
	if g.broker == "" {
		g.broker = os.Getenv("MQTT_BROKER")
	}
	if g.broker == "" {
		g.broker = "tcp://localhost:1883"
	}
	if g.node == "" {
		g.node = os.Getenv("SAUCEPAN_NODE_ID")
	}
}
