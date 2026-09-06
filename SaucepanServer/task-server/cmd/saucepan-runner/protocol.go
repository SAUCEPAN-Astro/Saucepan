// Command saucepan-runner is the sandboxed executor for researcher-authored
// on-pier code (#470 / #515). The pier agent forks one short-lived runner per
// captured frame; the runner holds no credentials, no hardware handle, and no
// network — its only channel is one Job on stdin and a stream of Record lines
// on stdout. The IPC contract itself lives in internal/pierjob (shared with
// cmd/pier-agent); this file just re-exports it under the names the runner's
// own code reads with, plus the sandbox wiring in sandbox.go / host.go.
// See docs/design/ON_PIER_SANDBOX_RUNTIME.md.
package main

import (
	"io"

	"github.com/saucepan/hotpath/internal/pierjob"
)

type (
	// RunnerJob / RunnerRecord are the pierjob IPC types.
	RunnerJob    = pierjob.Job
	RunnerRecord = pierjob.Record
	// PierSummary is one campaign pier roster entry served by list_piers.
	PierSummary = pierjob.PierSummary
)

const (
	ActionState = pierjob.ActionState
	ActionDone  = pierjob.ActionDone
	ActionError = pierjob.ActionError
)

func newRecordWriter(w io.Writer) *pierjob.RecordWriter { return pierjob.NewRecordWriter(w) }

func readJob(r io.Reader) (pierjob.Job, error) { return pierjob.ReadJob(r) }

func checkRecordGrant(rec pierjob.Record, grants map[string]bool) error {
	return pierjob.CheckRecordGrant(rec, grants)
}
