package main

// Black-box CLI tests for the schedsim command. main() is a thin flag-parse
// + dispatch wrapper around shared/schedsim (owned elsewhere, already unit
// tested in shared/schedsim/sim_test.go) — these tests exercise the CLI
// surface itself (flag handling, JSON shape, exit codes) by building and
// running the real binary, exactly the way the package doc's usage examples
// invoke it. No changes to main.go; this is a pure black-box harness.

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

var schedsimBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "schedsim-bin")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	schedsimBin = filepath.Join(dir, "schedsim")
	build := exec.Command("go", "build", "-o", schedsimBin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		panic("build schedsim binary: " + err.Error() + "\n" + string(out))
	}
	os.Exit(m.Run())
}

func run(t *testing.T, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	cmd := exec.Command(schedsimBin, args...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	code := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			t.Fatalf("run %v: %v", args, err)
		}
	}
	return outBuf.String(), errBuf.String(), code
}

// TestBaselineJSONShape covers the default scenario's --json report: every
// KPI field FormatReport's text mode also reports must be present and
// well-typed, since --json is the machine-readable contract other tooling
// (algorithm PR descriptions per the package doc) depends on.
func TestBaselineJSONShape(t *testing.T) {
	stdout, stderr, code := run(t, "-scenario", "baseline", "-json")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, stderr)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("output not valid JSON: %v\n%s", err, stdout)
	}
	for _, field := range []string{
		"frames_captured", "science_value", "deadline_misses",
		"node_utilization", "fairness_coefficient", "duplicate_assign_waves",
		"tasks_assigned", "tasks_unassigned",
	} {
		if _, ok := report[field]; !ok {
			t.Errorf("missing field %q in JSON report: %v", field, report)
		}
	}
}

// TestBaselineDeterministic is the "no ML" determinism check this scope
// exists to cover: the same scenario run twice, no seed/env/wall-clock
// input, must produce byte-identical JSON. schedsim's whole premise (#420
// doc comment) is a hermetic, reproducible KPI scorecard.
func TestBaselineDeterministic(t *testing.T) {
	out1, _, code1 := run(t, "-scenario", "baseline", "-json")
	out2, _, code2 := run(t, "-scenario", "baseline", "-json")
	if code1 != 0 || code2 != 0 {
		t.Fatalf("exit codes = %d, %d, want 0, 0", code1, code2)
	}
	if out1 != out2 {
		t.Fatalf("baseline scenario not deterministic:\nrun1: %s\nrun2: %s", out1, out2)
	}
}

// TestDefaultScenarioIsBaseline: no -scenario flag falls back to "baseline"
// per the flag.String default, and should behave identically to passing it
// explicitly.
func TestDefaultScenarioIsBaseline(t *testing.T) {
	explicit, _, codeExplicit := run(t, "-scenario", "baseline", "-json")
	implicit, _, codeImplicit := run(t, "-json")
	if codeExplicit != 0 || codeImplicit != 0 {
		t.Fatalf("exit codes = %d, %d, want 0, 0", codeExplicit, codeImplicit)
	}
	if explicit != implicit {
		t.Fatalf("default scenario diverges from explicit baseline:\ndefault: %s\nexplicit: %s", implicit, explicit)
	}
}

// TestTextReportNonEmpty covers the non-JSON path (FormatReport text mode),
// the default output shape when --json is omitted.
func TestTextReportNonEmpty(t *testing.T) {
	stdout, stderr, code := run(t, "-scenario", "baseline")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, stderr)
	}
	if stdout == "" {
		t.Fatal("text report was empty")
	}
}

// TestDuplicate400FixedModePasses: without -bug, the #400 replay must show
// zero duplicate-assign waves and therefore exit 0 (AssertNoDuplicateAssign
// succeeds) — see shared/schedsim TestDuplicateAssign_fixedModeNoWaves.
func TestDuplicate400FixedModePasses(t *testing.T) {
	stdout, stderr, code := run(t, "-scenario", "duplicate400", "-json")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (fixed mode should have 0 duplicate waves); stderr: %s\nstdout: %s", code, stderr, stdout)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("output not valid JSON: %v\n%s", err, stdout)
	}
	if dw, ok := report["duplicate_assign_waves"].(float64); !ok || dw != 0 {
		t.Fatalf("duplicate_assign_waves = %v, want 0", report["duplicate_assign_waves"])
	}
}

// TestDuplicate400BugModeStillRuns: with -bug the scenario intentionally
// reproduces the #400 defect and is expected to exit 0 regardless of
// duplicate waves (main.go only runs the assertion when !*bug).
func TestDuplicate400BugModeStillRuns(t *testing.T) {
	stdout, stderr, code := run(t, "-scenario", "duplicate400", "-bug", "-json")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, stderr)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("output not valid JSON: %v\n%s", err, stdout)
	}
}

// TestPlannedInterruptScenario covers the third scenario and its -lanes flag.
func TestPlannedInterruptScenario(t *testing.T) {
	for _, lanes := range []string{"true", "false"} {
		stdout, stderr, code := run(t, "-scenario", "planned421", "-lanes="+lanes, "-json")
		if code != 0 {
			t.Fatalf("lanes=%s: exit code = %d, want 0; stderr: %s", lanes, code, stderr)
		}
		var report map[string]any
		if err := json.Unmarshal([]byte(stdout), &report); err != nil {
			t.Fatalf("lanes=%s: output not valid JSON: %v\n%s", lanes, err, stdout)
		}
	}
}

// TestUnknownScenarioExitsTwo covers the default: switch branch — an
// unrecognized -scenario value must fail loudly (exit 2) rather than
// silently falling back to baseline.
func TestUnknownScenarioExitsTwo(t *testing.T) {
	_, stderr, code := run(t, "-scenario", "not-a-real-scenario")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if stderr == "" {
		t.Fatal("expected an error message on stderr for an unknown scenario")
	}
}
