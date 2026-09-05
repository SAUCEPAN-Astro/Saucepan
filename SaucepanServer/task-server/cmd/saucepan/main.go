// Command saucepan is a lightweight, MQTT-only monitoring CLI for
// Saucepan piers: pick which campaigns your scope contributes to, cap
// what it is allowed to do, watch what it is doing, and leave a note for
// whichever other piers share its current task (#463). Nothing else.
// No HTTP, no daemon.
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
	fmt.Fprint(w, `saucepan — MQTT-only pier monitoring CLI

Usage:
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
