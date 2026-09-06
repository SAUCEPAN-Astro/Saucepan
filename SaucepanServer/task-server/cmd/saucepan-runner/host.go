package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"

	"github.com/saucepan/hotpath/internal/pierjob"
	"github.com/saucepan/hotpath/shared/wire"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// hostModuleName is the wasm import module the runner provides. A researcher
// artifact may import functions from exactly this module and from
// wasi_snapshot_preview1 (see sandbox.go's import allow-list) — nothing else.
const hostModuleName = "saucepan"

// hostFuncNames is the exact set of functions the host module exports. The
// import allow-list in sandbox.go checks fetched modules against this.
var hostFuncNames = []string{"emit", "log", "frame_stat", "board_read", "list_piers", "recv"}

// maxStateBytes caps the opaque blob a researcher routine can carry to its
// next invocation via an ActionState record. prev_state is attacker-controlled
// on the way back in (ON_PIER_SANDBOX_RUNTIME.md §6) — the cap keeps a
// campaign from using it as unbounded pier-local storage.
const maxStateBytes = 64 << 10 // 64 KiB

// host carries everything the saucepan host functions need for one runner
// invocation. It never holds a hardware handle, a network client, or a
// credential — every effect leaves as a RunnerRecord on rw for the parent
// pier agent to carry out.
type host struct {
	job    RunnerJob
	grants map[string]bool
	rw     *pierjob.RecordWriter

	frame     *frameData // lazily loaded on first frame_stat
	frameErr  error
	frameDone bool

	ret    []byte // staged bytes for the next recv() drain (board_read / list_piers)
	retPos int

	sawError bool // an emit was malformed / a host op failed hard
}

func newHost(job RunnerJob, rw *pierjob.RecordWriter) *host {
	return &host{job: job, grants: job.Grants, rw: rw}
}

// register wires the six host functions onto a "saucepan" module builder.
func (h *host) register(ctx context.Context, r wazero.Runtime) error {
	_, err := r.NewHostModuleBuilder(hostModuleName).
		NewFunctionBuilder().WithFunc(h.emit).Export("emit").
		NewFunctionBuilder().WithFunc(h.log).Export("log").
		NewFunctionBuilder().WithFunc(h.frameStat).Export("frame_stat").
		NewFunctionBuilder().WithFunc(h.boardRead).Export("board_read").
		NewFunctionBuilder().WithFunc(h.listPiers).Export("list_piers").
		NewFunctionBuilder().WithFunc(h.recv).Export("recv").
		Instantiate(ctx)
	return err
}

// readMem copies [ptr,ptr+n) out of guest linear memory. A read outside the
// module's memory returns nil — the caller treats that as a rejected op, it
// never panics or reaches past the sandbox.
func readMem(m api.Module, ptr, n uint32) []byte {
	if n == 0 {
		return nil
	}
	b, ok := m.Memory().Read(ptr, n)
	if !ok {
		return nil
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}

// emit(ptr, len) -> i32 : the guest hands one JSON RunnerRecord. The host
// validates it against the grant map and the v1 action menu and, if it
// passes, forwards it to stdout for the pier agent. Return 0 = accepted,
// 1 = rejected (the guest may log but cannot force the record through).
func (h *host) emit(_ context.Context, m api.Module, ptr, n uint32) uint32 {
	raw := readMem(m, ptr, n)
	if raw == nil {
		h.sawError = true
		return 1
	}
	var rec RunnerRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		log.Printf("saucepan-runner: emit: bad record json: %v", err)
		h.sawError = true
		return 1
	}

	switch rec.Action {
	case ActionState:
		if len(rec.Payload) > maxStateBytes {
			log.Printf("saucepan-runner: emit: state payload %d bytes exceeds cap %d", len(rec.Payload), maxStateBytes)
			return 1
		}
		return h.forward(rec)
	case ActionDone, ActionError:
		// Terminal records are the runner's to emit, not the guest's.
		log.Printf("saucepan-runner: emit: guest may not emit %q", rec.Action)
		return 1
	}

	// A record the campaign was not granted, or one outside the v1 menu, is a
	// hard fail-closed: the run ends in an error record so the researcher gets
	// unambiguous feedback (the static checker + import allow-list should have
	// caught it far earlier).
	if err := checkRecordGrant(rec, h.grants); err != nil {
		log.Printf("saucepan-runner: emit: %v", err)
		h.sawError = true
		return 1
	}
	if rec.Action == wire.ActionNextCapture {
		var p wire.NextCapturePayload
		if len(rec.Payload) > 0 {
			if err := json.Unmarshal(rec.Payload, &p); err != nil {
				log.Printf("saucepan-runner: emit: next_capture payload: %v", err)
				h.sawError = true
				return 1
			}
		}
		if err := h.job.NextCaptureBounds.ValidateNextCapture(p); err != nil {
			log.Printf("saucepan-runner: emit: %v", err)
			h.sawError = true
			return 1
		}
	}
	return h.forward(rec)
}

func (h *host) forward(rec RunnerRecord) uint32 {
	if err := h.rw.Emit(rec); err != nil {
		log.Printf("saucepan-runner: emit: write record: %v", err)
		h.sawError = true
		return 1
	}
	return 0
}

// log(ptr, len) : guest debug string -> the runner's stderr. Kept off stdout
// so it never corrupts the RunnerRecord stream.
func (h *host) log(_ context.Context, m api.Module, ptr, n uint32) {
	if b := readMem(m, ptr, n); b != nil {
		log.Printf("saucepan-runner: [guest] %s", string(b))
	}
}

// frame_stat(keyPtr, keyLen) -> f64 : one scalar from the just-captured
// frame. Gated by read_frame; ungranted or unknown key -> NaN (the guest
// tests with a NaN check, never gets a misleading 0). See fitsread.go for
// the key set.
func (h *host) frameStat(_ context.Context, m api.Module, keyPtr, keyLen uint32) float64 {
	if !wire.GrantAllows(h.grants, wire.ActionReadFrame) {
		return math.NaN()
	}
	key := readMem(m, keyPtr, keyLen)
	if key == nil {
		return math.NaN()
	}
	if !h.frameDone {
		h.frameDone = true
		if h.job.FramePath == "" {
			h.frameErr = fmt.Errorf("no frame_path in job")
		} else {
			h.frame, h.frameErr = readFrame(h.job.FramePath)
		}
		if h.frameErr != nil {
			log.Printf("saucepan-runner: frame_stat: %v", h.frameErr)
		}
	}
	if h.frame == nil {
		return math.NaN()
	}
	return h.frame.stat(string(key))
}

// board_read() -> i32 : stages the recent campaign signal batch the pier
// agent passed in (RunnerJob.BoardNotes) as a JSON array and returns its byte
// length; the guest then drains it with recv(). Gated by board_read.
func (h *host) boardRead(context.Context, api.Module) uint32 {
	if !wire.GrantAllows(h.grants, wire.ActionBoardRead) {
		return 0
	}
	return h.stage(h.job.BoardNotes)
}

// list_piers() -> i32 : same pattern for the campaign pier roster. Gated by
// list_piers.
func (h *host) listPiers(context.Context, api.Module) uint32 {
	if !wire.GrantAllows(h.grants, wire.ActionListPiers) {
		return 0
	}
	return h.stage(h.job.CampaignPiers)
}

func (h *host) stage(v any) uint32 {
	b, err := json.Marshal(v)
	if err != nil {
		log.Printf("saucepan-runner: stage: %v", err)
		return 0
	}
	h.ret = b
	h.retPos = 0
	return uint32(len(b))
}

// recv(dstPtr, dstCap) -> i32 : copies up to dstCap bytes of the pending
// staged buffer into guest memory at dstPtr and advances; returns bytes
// copied, 0 when the buffer is exhausted. The guest calls it in a loop after
// board_read()/list_piers().
func (h *host) recv(_ context.Context, m api.Module, dstPtr, dstCap uint32) uint32 {
	if h.retPos >= len(h.ret) || dstCap == 0 {
		return 0
	}
	chunk := h.ret[h.retPos:]
	if uint32(len(chunk)) > dstCap {
		chunk = chunk[:dstCap]
	}
	if !m.Memory().Write(dstPtr, chunk) {
		return 0
	}
	h.retPos += len(chunk)
	return uint32(len(chunk))
}
