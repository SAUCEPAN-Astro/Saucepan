// Package pierjob is the process-local IPC contract between the pier agent
// (cmd/pier-agent) and the sandboxed on-pier code runner (cmd/saucepan-runner):
// one Job written to the runner's stdin, a stream of Record lines read back
// from its stdout. It is module-internal by design — not a wire contract, not
// something an SDK or an external tool ever sees. The runner holds no
// credentials, hardware handle, or network of its own; every capability it
// exposes to researcher code is answered by a field of Job (board_notes,
// campaign_piers, …) or surfaced as a Record for the agent to carry out.
// See docs/design/ON_PIER_SANDBOX_RUNTIME.md §4.
package pierjob

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"

	"github.com/saucepan/hotpath/shared/wire"
)

// Job is the single JSON object the agent writes to the runner's stdin.
type Job struct {
	CampaignID string `json:"campaign_id"`
	TaskID     string `json:"task_id,omitempty"`
	// FramePath is a read-only FITS the agent just wrote. The runner never
	// writes to disk.
	FramePath string `json:"frame_path,omitempty"`
	// ArtifactPath is the verified researcher wasm module to execute.
	ArtifactPath string `json:"artifact_path"`
	// ArtifactSHA256, when set, is the lowercase-hex content hash the agent
	// resolved from the assign (#518). The runner re-hashes ArtifactPath and
	// refuses to run on a mismatch — the independent second check at the
	// boundary that actually loads code.
	ArtifactSHA256 string `json:"artifact_sha256,omitempty"`
	// Grants maps an approved action name (wire.Action*) to whether this
	// campaign may emit it (#516). Absent/false = deny.
	Grants map[string]bool `json:"grants,omitempty"`
	// PierCodeDisabled is the campaign kill switch (#520). When true the runner
	// exits cleanly without loading the artifact.
	PierCodeDisabled bool `json:"pier_code_disabled,omitempty"`
	// PrevState is whatever the researcher code returned as a state Record on
	// the previous frame, or empty on the first. Opaque, attacker-controlled,
	// size-capped by the host — never parsed as anything privileged.
	PrevState json.RawMessage `json:"prev_state,omitempty"`
	// BoardNotes is the batch of recent campaign signals the agent drains just
	// before forking the runner; the board_read host function serves it. Gated
	// by wire.ActionBoardRead.
	BoardNotes []wire.BoardNote `json:"board_notes,omitempty"`
	// CampaignPiers is the campaign pier roster + online state; the list_piers
	// host function serves it. Gated by wire.ActionListPiers.
	CampaignPiers []PierSummary `json:"campaign_piers,omitempty"`
	// NextCaptureBounds are the campaign-declared limits an emitted next_capture
	// record is checked against, runner-side, before it leaves the sandbox. The
	// agent re-checks.
	NextCaptureBounds wire.NextCaptureBounds `json:"next_capture_bounds,omitempty"`
}

// PierSummary is one entry of Job.CampaignPiers.
type PierSummary struct {
	NodeID  string   `json:"node_id"`
	Online  bool     `json:"online"`
	SiteLat *float64 `json:"site_lat,omitempty"`
	SiteLon *float64 `json:"site_lon,omitempty"`
}

// Action names carried in Record.Action. The grantable set is
// wire.PierCodeActions; these three are the runner's own terminal records.
const (
	ActionState = "state"
	ActionDone  = "done"
	ActionError = "error"
)

// Record is one line of the runner's stdout. The agent validates every record
// against Job.Grants and the v1 menu and fails closed on anything
// unrecognised or ungranted before acting on it.
type Record struct {
	Action  string          `json:"action"`
	Payload json.RawMessage `json:"payload,omitempty"`
	OK      bool            `json:"ok,omitempty"`  // ActionDone
	Msg     string          `json:"msg,omitempty"` // ActionError / skip reason
}

// ReadJob decodes exactly one Job from r; trailing data is an error.
func ReadJob(r io.Reader) (Job, error) {
	dec := json.NewDecoder(r)
	var job Job
	if err := dec.Decode(&job); err != nil {
		return Job{}, fmt.Errorf("decode job: %w", err)
	}
	if dec.More() {
		return Job{}, fmt.Errorf("decode job: unexpected trailing data on stdin")
	}
	return job, nil
}

// RecordWriter emits Records as newline-delimited compact JSON.
type RecordWriter struct{ w *bufio.Writer }

func NewRecordWriter(w io.Writer) *RecordWriter { return &RecordWriter{w: bufio.NewWriter(w)} }

func (rw *RecordWriter) Emit(rec Record) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if _, err := rw.w.Write(b); err != nil {
		return err
	}
	if err := rw.w.WriteByte('\n'); err != nil {
		return err
	}
	return rw.w.Flush()
}

func (rw *RecordWriter) Done()           { _ = rw.Emit(Record{Action: ActionDone, OK: true}) }
func (rw *RecordWriter) Fail(msg string) { _ = rw.Emit(Record{Action: ActionError, Msg: msg}) }

// Skip is a clean, deliberate no-run (kill switch set, or nothing to run).
func (rw *RecordWriter) Skip(reason string) {
	_ = rw.Emit(Record{Action: ActionDone, OK: true, Msg: reason})
}

// CheckRecordGrant returns an error unless rec is an action grants permits.
// Terminal records (state / done / error) always pass. Any action not in the
// v1 wire.PierCodeActions menu, or in it but not granted, fails closed. Both
// the runner (before writing a record) and the agent (before acting on one)
// call this — defence in depth.
func CheckRecordGrant(rec Record, grants map[string]bool) error {
	switch rec.Action {
	case ActionState, ActionDone, ActionError:
		return nil
	}
	if !wire.IsPierCodeAction(rec.Action) {
		return fmt.Errorf("record action %q is not in the v1 pier-code menu", rec.Action)
	}
	if !wire.GrantAllows(grants, rec.Action) {
		return fmt.Errorf("record action %q not granted to this campaign", rec.Action)
	}
	return nil
}
