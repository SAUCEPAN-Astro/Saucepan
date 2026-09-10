package main

import (
	"flag"
	"os"
	"strings"
	"testing"
)

// TestRunDispatch covers run()'s command dispatch for the paths that don't
// require a live MQTT broker: no args, help, and an unknown command. The
// four real subcommands (status/constraints/projects/board) all connect()
// to a broker before doing anything else, so they're covered at the
// session/command-function level elsewhere in this package rather than
// through run() end-to-end (no broker is available in this test
// environment — see PIER_CLI.md and the fakeMQTTClient tests).
func TestRunDispatch(t *testing.T) {
	t.Run("no args prints usage to stderr and exits error", func(t *testing.T) {
		if got := run(nil); got != exitError {
			t.Fatalf("run(nil) = %d, want exitError(%d)", got, exitError)
		}
	})

	t.Run("empty args slice behaves like nil", func(t *testing.T) {
		if got := run([]string{}); got != exitError {
			t.Fatalf("run([]) = %d, want exitError(%d)", got, exitError)
		}
	})

	for _, helpArg := range []string{"-h", "--help", "help"} {
		t.Run("help via "+helpArg, func(t *testing.T) {
			if got := run([]string{helpArg}); got != exitOK {
				t.Fatalf("run([%s]) = %d, want exitOK(%d)", helpArg, got, exitOK)
			}
		})
	}

	t.Run("unknown command exits error", func(t *testing.T) {
		if got := run([]string{"not-a-real-command"}); got != exitError {
			t.Fatalf("run([not-a-real-command]) = %d, want exitError(%d)", got, exitError)
		}
	})
}

func TestUsageMentionsAllFourCommands(t *testing.T) {
	out := captureStdout(t, func() { usage(os.Stdout) })
	for _, cmd := range []string{"status", "constraints", "projects", "board"} {
		if !strings.Contains(out, cmd) {
			t.Errorf("usage text missing subcommand %q:\n%s", cmd, out)
		}
	}
	if !strings.Contains(out, "--json") {
		t.Error("usage text should mention --json as a global flag")
	}
}

func TestBindGlobalFlagsDefaults(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	g := bindGlobalFlags(fs)
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if g.json {
		t.Error("json should default to false")
	}
	if g.broker != "" {
		t.Errorf("broker should default to empty (resolved later), got %q", g.broker)
	}
	if g.timeout.Seconds() != 5 {
		t.Errorf("default timeout = %v, want 5s", g.timeout)
	}
}

func TestGlobalFlagsResolve(t *testing.T) {
	t.Run("broker falls back to env then hardcoded default", func(t *testing.T) {
		t.Setenv("MQTT_BROKER", "")
		g := &globalFlags{}
		g.resolve()
		if g.broker != "tcp://localhost:1883" {
			t.Fatalf("broker = %q, want the hardcoded default", g.broker)
		}
	})

	t.Run("env broker used when flag unset", func(t *testing.T) {
		t.Setenv("MQTT_BROKER", "tcp://envhost:1883")
		g := &globalFlags{}
		g.resolve()
		if g.broker != "tcp://envhost:1883" {
			t.Fatalf("broker = %q, want env value", g.broker)
		}
	})

	t.Run("explicit flag wins over env", func(t *testing.T) {
		t.Setenv("MQTT_BROKER", "tcp://envhost:1883")
		g := &globalFlags{broker: "tcp://flaghost:1883"}
		g.resolve()
		if g.broker != "tcp://flaghost:1883" {
			t.Fatalf("broker = %q, want flag value to win", g.broker)
		}
	})

	t.Run("node falls back to env", func(t *testing.T) {
		t.Setenv("SAUCEPAN_NODE_ID", "env-node")
		g := &globalFlags{}
		g.resolve()
		if g.node != "env-node" {
			t.Fatalf("node = %q, want env-node", g.node)
		}
	})

	t.Run("explicit node flag wins over env", func(t *testing.T) {
		t.Setenv("SAUCEPAN_NODE_ID", "env-node")
		g := &globalFlags{node: "flag-node"}
		g.resolve()
		if g.node != "flag-node" {
			t.Fatalf("node = %q, want flag-node", g.node)
		}
	})
}
