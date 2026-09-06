package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saucepan/hotpath/shared/fitswrite"
	"github.com/saucepan/hotpath/shared/wire"
)

// emitImport is the saucepan.emit host-function shape the fixtures import.
var emitImport = wasmImport{module: hostModuleName, field: "emit", params: 2, results: 1}

// writeArtifact drops b at a temp path and returns it.
func writeArtifact(t *testing.T, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "artifact.wasm")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func runJob(t *testing.T, job RunnerJob) (int, []RunnerRecord) {
	t.Helper()
	if job.ArtifactSHA256 == "" && job.ArtifactPath != "" {
		artifact, err := os.ReadFile(job.ArtifactPath)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(artifact)
		job.ArtifactSHA256 = hex.EncodeToString(sum[:])
	}
	var out bytes.Buffer
	code := runArtifact(context.Background(), job, newRecordWriter(&out))
	return code, decodeRecords(t, out.String())
}

func TestSandboxRunsModuleAndForwardsGrantedEmit(t *testing.T) {
	blob := []byte(`{"action":"board_post","payload":{"message":"tile 2 clear"}}`)
	mod := buildWasm([]wasmImport{emitImport}, blob, emitCallBody(len(blob)), true)

	code, recs := runJob(t, RunnerJob{
		CampaignID:   "camp-1",
		ArtifactPath: writeArtifact(t, mod),
		Grants:       map[string]bool{wire.ActionBoardPost: true},
	})
	if code != 0 {
		t.Fatalf("exit = %d, want 0; recs=%+v", code, recs)
	}
	if len(recs) != 2 {
		t.Fatalf("want [board_post, done], got %+v", recs)
	}
	if recs[0].Action != wire.ActionBoardPost {
		t.Fatalf("first record = %q, want board_post", recs[0].Action)
	}
	var p struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(recs[0].Payload, &p); err != nil || p.Message != "tile 2 clear" {
		t.Fatalf("payload passthrough failed: %s (%v)", recs[0].Payload, err)
	}
	if recs[1].Action != ActionDone || !recs[1].OK {
		t.Fatalf("last record = %+v, want done/ok", recs[1])
	}
}

func TestSandboxRejectsEmitOfUngrantedAction(t *testing.T) {
	blob := []byte(`{"action":"next_capture","payload":{"exposure_sec":30}}`)
	mod := buildWasm([]wasmImport{emitImport}, blob, emitCallBody(len(blob)), true)

	code, recs := runJob(t, RunnerJob{
		CampaignID:   "camp-1",
		ArtifactPath: writeArtifact(t, mod),
		Grants:       map[string]bool{wire.ActionBoardPost: true}, // no next_capture
	})
	if code != 1 {
		t.Fatalf("exit = %d, want 1; recs=%+v", code, recs)
	}
	last := recs[len(recs)-1]
	if last.Action != ActionError {
		t.Fatalf("last record = %+v, want error", last)
	}
}

func TestSandboxRejectsDisallowedImportModule(t *testing.T) {
	mod := buildWasm([]wasmImport{{module: "env", field: "sneaky", params: 0, results: 0}}, nil, noopBody(), true)

	code, recs := runJob(t, RunnerJob{CampaignID: "c", ArtifactPath: writeArtifact(t, mod)})
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if len(recs) != 1 || recs[0].Action != ActionError || !strings.Contains(recs[0].Msg, "env.sneaky") {
		t.Fatalf("want one error naming env.sneaky, got %+v", recs)
	}
}

func TestSandboxRejectsUnknownSaucepanImport(t *testing.T) {
	mod := buildWasm([]wasmImport{{module: hostModuleName, field: "exfiltrate", params: 0, results: 0}}, nil, noopBody(), true)

	code, recs := runJob(t, RunnerJob{CampaignID: "c", ArtifactPath: writeArtifact(t, mod)})
	if code != 1 || recs[0].Action != ActionError || !strings.Contains(recs[0].Msg, "saucepan.exfiltrate") {
		t.Fatalf("want one error naming saucepan.exfiltrate, got code=%d %+v", code, recs)
	}
}

func TestSandboxRejectsModuleWithNoRunExport(t *testing.T) {
	blob := []byte(`{"action":"board_post","payload":{}}`)
	mod := buildWasm([]wasmImport{emitImport}, blob, emitCallBody(len(blob)), false)

	code, recs := runJob(t, RunnerJob{
		CampaignID:   "c",
		ArtifactPath: writeArtifact(t, mod),
		Grants:       map[string]bool{wire.ActionBoardPost: true},
	})
	if code != 1 || recs[0].Action != ActionError || !strings.Contains(recs[0].Msg, "run") {
		t.Fatalf("want one error about missing run, got code=%d %+v", code, recs)
	}
}

func TestSandboxAllowsMemoryAboveFormerLimit(t *testing.T) {
	// 320 pages is 20 MiB, above the former 16 MiB application-level cap.
	// Keep this regression test so a runtime memory limit is not reintroduced
	// accidentally while the other sandbox gates remain in place.
	mod := buildWasmMem(nil, nil, noopBody(), true, 320)
	code, recs := runJob(t, RunnerJob{CampaignID: "c", ArtifactPath: writeArtifact(t, mod)})
	if code != 0 || recs[len(recs)-1].Action != ActionDone {
		t.Fatalf("module above the former memory cap should run, got code=%d %+v", code, recs)
	}
}

func TestSandboxRejectsGarbageArtifact(t *testing.T) {
	code, recs := runJob(t, RunnerJob{
		CampaignID:   "c",
		ArtifactPath: writeArtifact(t, []byte("\x00asm not a real module")),
	})
	if code != 1 || recs[0].Action != ActionError || !strings.Contains(recs[0].Msg, "compile") {
		t.Fatalf("want one compile error, got code=%d %+v", code, recs)
	}
}

// --- frame reader (pure, no wasm) -------------------------------------------

func TestReadFrameStats(t *testing.T) {
	pix := [][]float64{
		{10, 20, 30},
		{40, 50, 60},
	}
	h := fitswrite.NewHeader()
	h.SetFloat("EXPTIME", 12.5, "s")
	path := filepath.Join(t.TempDir(), "f.fits")
	if err := fitswrite.WriteImage(path, pix, h); err != nil {
		t.Fatal(err)
	}

	fd, err := readFrame(path)
	if err != nil {
		t.Fatalf("readFrame: %v", err)
	}
	if fd.width != 3 || fd.height != 2 {
		t.Fatalf("dims = %dx%d, want 3x2", fd.width, fd.height)
	}
	if got := fd.stat("mean"); got != 35 {
		t.Fatalf("mean = %v, want 35", got)
	}
	if got := fd.stat("min"); got != 10 {
		t.Fatalf("min = %v, want 10", got)
	}
	if got := fd.stat("max"); got != 60 {
		t.Fatalf("max = %v, want 60", got)
	}
	if got := fd.stat("median"); got != 35 {
		t.Fatalf("median = %v, want 35", got)
	}
	if got := fd.stat("hdr:EXPTIME"); got != 12.5 {
		t.Fatalf("hdr:EXPTIME = %v, want 12.5", got)
	}
	if got := fd.stat("hdr:NOPE"); !math.IsNaN(got) {
		t.Fatalf("unknown header = %v, want NaN", got)
	}
	if got := fd.stat("bogus"); !math.IsNaN(got) {
		t.Fatalf("unknown key = %v, want NaN", got)
	}
}

// --- host read-op gating (nil module: grant check happens first) -----------

func TestHostReadOpsGateOnGrants(t *testing.T) {
	job := RunnerJob{
		BoardNotes:    []wire.BoardNote{{NodeID: "pier_a", Message: "hi"}},
		CampaignPiers: []PierSummary{{NodeID: "pier_a", Online: true}},
		Grants:        map[string]bool{}, // nothing granted
	}
	h := newHost(job, newRecordWriter(&bytes.Buffer{}))

	if n := h.boardRead(context.Background(), nil); n != 0 {
		t.Fatalf("board_read without grant returned %d, want 0", n)
	}
	if n := h.listPiers(context.Background(), nil); n != 0 {
		t.Fatalf("list_piers without grant returned %d, want 0", n)
	}
	if got := h.frameStat(context.Background(), nil, 0, 4); !math.IsNaN(got) {
		t.Fatalf("frame_stat without grant = %v, want NaN", got)
	}

	h.grants = map[string]bool{wire.ActionBoardRead: true, wire.ActionListPiers: true}
	if n := h.boardRead(context.Background(), nil); n == 0 {
		t.Fatalf("board_read with grant staged nothing")
	}
	if !bytes.Contains(h.ret, []byte("pier_a")) {
		t.Fatalf("staged board bytes missing note: %s", h.ret)
	}
	if n := h.listPiers(context.Background(), nil); n == 0 {
		t.Fatalf("list_piers with grant staged nothing")
	}
}
