package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/saucepan/hotpath/shared/fitswrite"
	"github.com/saucepan/hotpath/shared/wire"
)

// TestGoldenCompiledModuleRunsEndToEnd loads a wasm module produced by the SDK
// compiler (`saucepan.pier.build`, checked in under testdata/) and runs it
// through the real sandbox. It proves the SDK-emitted binary is accepted by
// wazero, imports only the host ABI, and drives the host functions as intended.
//
// Regenerate testdata/saturation_detector.wasm with:
//
//	cd SaucepanSDK/python && python - <<'PY'
//	from saucepan.pier import build
//	open("../../SaucepanServer/task-server/cmd/saucepan-runner/testdata/saturation_detector.wasm","wb").write(
//	    build(open("../../SaucepanServer/task-server/cmd/saucepan-runner/testdata/saturation_detector.py").read()))
//	PY
func TestGoldenCompiledModuleRunsEndToEnd(t *testing.T) {
	mod, err := os.ReadFile(filepath.Join("testdata", "saturation_detector.wasm"))
	if err != nil {
		t.Fatalf("read golden wasm: %v", err)
	}

	grants := map[string]bool{
		wire.ActionReadFrame:   true,
		wire.ActionBoardPost:   true,
		wire.ActionNextCapture: true,
	}

	t.Run("bright frame emits board_post + next_capture", func(t *testing.T) {
		frame := writeFrame(t, 65000)
		code, recs := runJob(t, RunnerJob{
			CampaignID:   "camp-golden",
			ArtifactPath: writeArtifact(t, mod),
			FramePath:    frame,
			Grants:       grants,
		})
		if code != 0 {
			t.Fatalf("exit = %d, recs=%+v", code, recs)
		}
		actions := recordActions(recs)
		wantPrefix := []string{wire.ActionBoardPost, wire.ActionNextCapture, ActionDone}
		if !equalStrings(actions, wantPrefix) {
			t.Fatalf("actions = %v, want %v", actions, wantPrefix)
		}
		if msg := boardMessage(t, recs[0]); msg != "saturated frame" {
			t.Fatalf("board_post message = %q, want %q", msg, "saturated frame")
		}
	})

	t.Run("dark frame takes the elif branch", func(t *testing.T) {
		frame := writeFrame(t, 200)
		code, recs := runJob(t, RunnerJob{
			CampaignID:   "camp-golden",
			ArtifactPath: writeArtifact(t, mod),
			FramePath:    frame,
			Grants:       grants,
		})
		if code != 0 {
			t.Fatalf("exit = %d, recs=%+v", code, recs)
		}
		actions := recordActions(recs)
		if !equalStrings(actions, []string{wire.ActionBoardPost, ActionDone}) {
			t.Fatalf("actions = %v, want [board_post done]", actions)
		}
		if msg := boardMessage(t, recs[0]); msg != "dark frame" {
			t.Fatalf("board_post message = %q, want %q", msg, "dark frame")
		}
	})

	t.Run("mid frame emits nothing but done", func(t *testing.T) {
		frame := writeFrame(t, 3000)
		code, recs := runJob(t, RunnerJob{
			CampaignID:   "camp-golden",
			ArtifactPath: writeArtifact(t, mod),
			FramePath:    frame,
			Grants:       grants,
		})
		if code != 0 {
			t.Fatalf("exit = %d, recs=%+v", code, recs)
		}
		if !equalStrings(recordActions(recs), []string{ActionDone}) {
			t.Fatalf("actions = %v, want [done]", recordActions(recs))
		}
	})

	t.Run("read_frame denied when grant withheld", func(t *testing.T) {
		frame := writeFrame(t, 65000)
		code, recs := runJob(t, RunnerJob{
			CampaignID:   "camp-golden",
			ArtifactPath: writeArtifact(t, mod),
			FramePath:    frame,
			Grants:       map[string]bool{wire.ActionBoardPost: true},
		})
		// frame_stat returns NaN under a withheld read_frame grant, so the
		// `mean > 5000` test is false and neither branch fires; the module
		// still finishes cleanly.
		if code != 0 {
			t.Fatalf("exit = %d, recs=%+v", code, recs)
		}
		if !equalStrings(recordActions(recs), []string{ActionDone}) {
			t.Fatalf("actions = %v, want [done] (NaN compare short-circuits)", recordActions(recs))
		}
	})
}

// writeFrame drops a 4x4 FITS frame whose pixels are all v, so mean==v.
func writeFrame(t *testing.T, v float64) string {
	t.Helper()
	px := make([][]float64, 4)
	for y := range px {
		px[y] = []float64{v, v, v, v}
	}
	p := filepath.Join(t.TempDir(), "frame.fits")
	if err := fitswrite.WriteImage(p, px, fitswrite.NewHeader()); err != nil {
		t.Fatal(err)
	}
	return p
}

func recordActions(recs []RunnerRecord) []string {
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.Action
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func boardMessage(t *testing.T, rec RunnerRecord) string {
	t.Helper()
	var p struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Payload, &p); err != nil {
		t.Fatalf("decode board payload %s: %v", rec.Payload, err)
	}
	return p.Message
}
