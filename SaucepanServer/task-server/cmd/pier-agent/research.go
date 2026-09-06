package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/saucepan/hotpath/internal/pierjob"
	"github.com/saucepan/hotpath/shared/wire"
)

// pierCode holds the pier-agent side of on-pier researcher code (#470): it
// forks cmd/saucepan-runner once per captured frame for a campaign that ships
// code, then carries out the effects the sandbox asked for (a board post, a
// next-capture nudge, an inbox alert). It is the only component that ever
// holds an MQTT handle or an API token — the runner has neither. Zero value =
// feature off (a pier not set up to run researcher code just skips it).
type pierCode struct {
	// RunnerPath is the saucepan-runner binary. Empty disables the feature.
	RunnerPath string
	// CacheDir is where wire.FetchVerifiedArtifact writes <sha256>.wasm.
	CacheDir string
	// RunTimeout wall-clock-bounds one runner process (belt to the runner's
	// own in-process hang guard). Default 90s.
	RunTimeout time.Duration
	// PostBoardNote publishes a BoardNote to the opaque campaign message
	// stream. EventType/Payload remain compatibility metadata for older typed
	// actions; the transport does not interpret them. Injected by main so this
	// stays testable without a broker.
	PostBoardNote func(campaignID, nodeID string, note wire.BoardNote) error

	// state carries a campaign's opaque blob between frames.
	state map[string]json.RawMessage
	// pendingCapture is keyed by campaign so one campaign's next_capture record
	// cannot change another campaign's next exposure.
	pendingCapture map[string]*wire.NextCapturePayload
}

func (pc *pierCode) enabled() bool { return pc != nil && pc.RunnerPath != "" }

// run executes the campaign's artifact against framePath and applies every
// record the sandbox emits. A nil error means the sandbox finished cleanly
// (it may still have had individual records rejected — those are logged).
func (pc *pierCode) run(ctx context.Context, nodeID string, assign wire.AssignTaskPayload, framePath string, board []wire.BoardNote, piers []pierjob.PierSummary) error {
	if !pc.enabled() || assign.PierCode == nil || assign.PierCodeDisabled {
		return nil
	}
	if pc.state == nil {
		pc.state = map[string]json.RawMessage{}
	}

	grants := assign.PierCodeGrants
	if grants == nil {
		grants = wire.DefaultPierCodeGrants()
	}

	artifactPath, err := wire.FetchVerifiedArtifact(assign.PierCode, pc.CacheDir, wire.HTTPGet)
	if err != nil {
		return fmt.Errorf("pier_code: fetch artifact for campaign %s: %w", assign.CampaignID, err)
	}

	job := pierjob.Job{
		CampaignID:        assign.CampaignID,
		TaskID:            strconv.Itoa(assign.TaskID),
		FramePath:         framePath,
		ArtifactPath:      artifactPath,
		ArtifactSHA256:    assign.PierCode.SHA256,
		Grants:            grants,
		PierCodeDisabled:  assign.PierCodeDisabled,
		PrevState:         pc.state[assign.CampaignID],
		BoardNotes:        board,
		CampaignPiers:     piers,
		NextCaptureBounds: nextCaptureBounds(assign),
	}
	jobJSON, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("pier_code: marshal job: %w", err)
	}

	timeout := pc.RunTimeout
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, pc.RunnerPath)
	// The runner is intentionally credential-less. It receives only the
	// serialized job over stdin; no pier-agent environment variable should
	// cross the process boundary.
	cmd.Env = []string{}
	cmd.Stdin = bytes.NewReader(jobJSON)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = log.Writer()
	runErr := cmd.Run() // non-zero exit is expected on a runner-side error record

	recs, perr := parseRecords(stdout.Bytes())
	if perr != nil {
		return fmt.Errorf("pier_code: runner output for campaign %s: %w (exit: %v)", assign.CampaignID, perr, runErr)
	}
	var applyErr error
	for _, rec := range recs {
		if err := pc.apply(nodeID, assign, grants, rec); err != nil {
			log.Printf("pier-agent: pier_code: campaign %s: %v", assign.CampaignID, err)
			if applyErr == nil {
				applyErr = err
			}
		}
	}
	if applyErr == nil && runErr != nil && len(recs) == 0 {
		return fmt.Errorf("pier_code: runner for campaign %s exited %v with no records", assign.CampaignID, runErr)
	}
	return applyErr
}

func parseRecords(out []byte) ([]pierjob.Record, error) {
	var recs []pierjob.Record
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		var rec pierjob.Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			return nil, fmt.Errorf("bad record line %q: %w", line, err)
		}
		recs = append(recs, rec)
	}
	return recs, nil
}

// apply carries out one record. Terminal records manage carried state and
// surface a runner error; every grantable record is grant-checked again here
// (defence in depth) before it has any effect.
func (pc *pierCode) apply(nodeID string, assign wire.AssignTaskPayload, grants map[string]bool, rec pierjob.Record) error {
	switch rec.Action {
	case pierjob.ActionState:
		if len(rec.Payload) <= 64<<10 {
			pc.state[assign.CampaignID] = append(json.RawMessage(nil), rec.Payload...)
		}
		return nil
	case pierjob.ActionDone:
		return nil
	case pierjob.ActionError:
		return fmt.Errorf("runner reported: %s", rec.Msg)
	}

	if err := pierjob.CheckRecordGrant(rec, grants); err != nil {
		return err
	}

	switch rec.Action {
	case wire.ActionBoardPost:
		if pc.PostBoardNote == nil {
			return fmt.Errorf("board_post record but no board publisher wired")
		}
		var p struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(rec.Payload, &p)
		note := wire.BoardNote{
			CampaignID: assign.CampaignID,
			NodeID:     nodeID,
			MessageID:  wire.NewMessageID(),
			Message:    p.Message,
			EventType:  "note",
			SentAt:     time.Now().UTC(),
		}
		return pc.PostBoardNote(assign.CampaignID, nodeID, note)

	case wire.ActionInboxAlert, wire.ActionUrgencyFlag, wire.ActionRequestTime:
		if pc.PostBoardNote == nil {
			return fmt.Errorf("%s record but no board publisher wired", rec.Action)
		}
		var p struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(rec.Payload, &p)
		note := wire.BoardNote{
			CampaignID: assign.CampaignID,
			NodeID:     nodeID,
			MessageID:  wire.NewMessageID(),
			Message:    p.Message,
			EventType:  boardEventType(rec.Action),
			Payload:    rec.Payload,
			SentAt:     time.Now().UTC(),
		}
		return pc.PostBoardNote(assign.CampaignID, nodeID, note)

	case wire.ActionNextCapture:
		var p wire.NextCapturePayload
		if err := json.Unmarshal(rec.Payload, &p); err != nil {
			return fmt.Errorf("next_capture payload: %w", err)
		}
		if err := nextCaptureBounds(assign).ValidateNextCapture(p); err != nil {
			return err
		}
		if pc.pendingCapture == nil {
			pc.pendingCapture = map[string]*wire.NextCapturePayload{}
		}
		pc.pendingCapture[assign.CampaignID] = &p
		return nil

	case wire.ActionReadFrame, wire.ActionBoardRead, wire.ActionListPiers:
		// Pull-only actions: the guest gets these through host functions, it
		// does not emit them as records. Ignore defensively.
		log.Printf("pier-agent: pier_code: ignoring emitted pull action %q", rec.Action)
		return nil
	}
	return fmt.Errorf("unhandled granted action %q", rec.Action)
}

// boardEventType maps an on-pier action name to the BoardNote.EventType the
// collector's bridge keys on when mirroring to the researcher HTTP board.
func boardEventType(action string) string {
	switch action {
	case wire.ActionInboxAlert:
		return "alert"
	case wire.ActionUrgencyFlag:
		return "urgency"
	case wire.ActionRequestTime:
		return "request_time"
	default:
		return "note"
	}
}

// takePendingCapture returns and clears any next_capture override. The capture
// path calls it once per exposure and re-checks it against the current
// assignment: a campaign can receive a later assignment with narrower bounds.
func (pc *pierCode) takePendingCapture(campaignID string, bounds wire.NextCaptureBounds, allowed bool) (*wire.NextCapturePayload, error) {
	if pc == nil {
		return nil, nil
	}
	p := pc.pendingCapture[campaignID]
	delete(pc.pendingCapture, campaignID)
	if p == nil || !allowed {
		return nil, nil
	}
	if err := bounds.ValidateNextCapture(*p); err != nil {
		return nil, err
	}
	return p, nil
}

func nextCaptureAllowed(assign wire.AssignTaskPayload) bool {
	if assign.PierCode == nil || assign.PierCodeDisabled {
		return false
	}
	grants := assign.PierCodeGrants
	if grants == nil {
		grants = wire.DefaultPierCodeGrants()
	}
	return wire.GrantAllows(grants, wire.ActionNextCapture)
}

// nextCaptureBounds derives the narrowest bounds currently present in the
// assign payload. Until a future wire revision carries separate campaign
// bounds, a researcher routine may only keep this task's exposure at or below
// its declared integration time and may not change its declared filter set.
func nextCaptureBounds(assign wire.AssignTaskPayload) wire.NextCaptureBounds {
	maxExposure := assign.IntegrationTime
	if maxExposure <= 0 {
		maxExposure = 30
	}
	return wire.NextCaptureBounds{
		MaxExposureSec: maxExposure,
		AllowedFilters: append([]string(nil), assign.RequiredFilters...),
	}
}
