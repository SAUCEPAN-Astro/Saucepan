package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

// captureStdout redirects os.Stdout for the duration of fn and returns
// everything written to it. Needed because emit()/newTable() write straight
// to os.Stdout rather than an injectable io.Writer (no such seam exists in
// this small CLI — see output.go), so this is the only way to assert on
// human-readable and --json rendering without changing production code.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	return buf.String()
}

func TestEmitJSONMode(t *testing.T) {
	type payload struct {
		A string `json:"a"`
		B int    `json:"b"`
	}
	humanCalled := false
	out := captureStdout(t, func() {
		emit(true, payload{A: "x", B: 3}, func() { humanCalled = true })
	})
	if humanCalled {
		t.Fatal("human renderer must not run when jsonMode is true")
	}
	var got payload
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("emit(json) output not valid JSON: %v\n%s", err, out)
	}
	if got.A != "x" || got.B != 3 {
		t.Fatalf("emit(json) round-trip = %+v, want {x 3}", got)
	}
	// json.Encoder always appends a trailing newline.
	if !strings.HasSuffix(out, "\n") {
		t.Fatal("expected JSON output to end with a newline")
	}
}

func TestEmitHumanMode(t *testing.T) {
	humanCalled := false
	out := captureStdout(t, func() {
		emit(false, "unused", func() {
			humanCalled = true
			os.Stdout.WriteString("human-output-marker\n")
		})
	})
	if !humanCalled {
		t.Fatal("human renderer must run when jsonMode is false")
	}
	if !strings.Contains(out, "human-output-marker") {
		t.Fatalf("expected human renderer's output in captured stdout, got %q", out)
	}
}

func TestTableRendering(t *testing.T) {
	out := captureStdout(t, func() {
		tbl := newTable()
		tbl.row("A", "BB", "CCC")
		tbl.row("1", "22", "333")
		tbl.flush()
	})
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 rendered lines, got %d: %q", len(lines), out)
	}
	for _, l := range lines {
		if !strings.Contains(l, "\t") && !strings.Contains(l, "  ") {
			// tabwriter converts tabs to aligned spaces on flush; either
			// raw tabs or the padded columns are acceptable evidence of
			// column separation.
			t.Fatalf("row does not look column-separated: %q", l)
		}
	}
}

func TestTableEmptyRow(t *testing.T) {
	// A row with no columns must not panic and produces a blank line.
	out := captureStdout(t, func() {
		tbl := newTable()
		tbl.row()
		tbl.flush()
	})
	if out != "\n" {
		t.Fatalf("empty row = %q, want a single newline", out)
	}
}
