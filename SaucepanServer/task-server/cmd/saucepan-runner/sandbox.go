package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/saucepan/hotpath/internal/pierjob"
	"github.com/saucepan/hotpath/shared/wire"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// runnerHangGuard bounds one module invocation in wall-clock time. It is a
// liveness guard so a wedged or spinning artifact cannot stall the pier's
// capture loop. wazero has no instruction/fuel metering, so this — not a CPU
// budget — is the bound on compute; 60s is comfortably above a cold start +
// a summary-statistic pass and well under the ≥60s telemetry cadence.
const runnerHangGuard = 60 * time.Second

// runArtifact loads job.ArtifactPath as a wasm32 module under wazero, checks
// its imports against the allow-list, wires the saucepan host functions gated
// by job.Grants, and calls the module's exported `run` once. Every effect the
// module asks for leaves as a RunnerRecord on rw; runArtifact emits the
// terminal done/error record itself and returns the process exit code.
func runArtifact(parent context.Context, job RunnerJob, rw *pierjob.RecordWriter) int {
	artifact, err := os.ReadFile(job.ArtifactPath)
	if err != nil {
		rw.Fail("read artifact: " + err.Error())
		return 1
	}
	ref := &wire.PierCodeRef{SHA256: job.ArtifactSHA256}
	if err := ref.VerifyArtifactBytes(artifact); err != nil {
		rw.Fail("verify artifact: " + err.Error())
		return 1
	}

	ctx, cancel := context.WithTimeout(parent, runnerHangGuard)
	defer cancel()

	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().
		WithCloseOnContextDone(true))
	defer rt.Close(ctx)

	// WASI is instantiated so a module built for wasm32-wasi links, but the
	// ModuleConfig below grants it no filesystem, no args, no env and a
	// discarded stdout — path_open/sock_* have nothing to open. wazero's WASI
	// is itself the fence here.
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		rw.Fail("instantiate wasi: " + err.Error())
		return 1
	}

	compiled, err := rt.CompileModule(ctx, artifact)
	if err != nil {
		rw.Fail("compile artifact: " + err.Error())
		return 1
	}
	if err := checkImports(compiled); err != nil {
		rw.Fail(err.Error())
		return 1
	}
	if _, ok := compiled.ExportedFunctions()["run"]; !ok {
		rw.Fail("artifact has no exported `run` function")
		return 1
	}

	h := newHost(job, rw)
	if err := h.register(ctx, rt); err != nil {
		rw.Fail("register host module: " + err.Error())
		return 1
	}

	cfg := wazero.NewModuleConfig().
		WithName("").
		WithStdout(io.Discard).
		WithStderr(os.Stderr).
		WithArgs().
		WithSysNanotime().
		WithSysNanosleep()
		// no WithFSConfig, no WithEnv, no WithRandSource: the module gets no
		// filesystem, no environment, no randomness source.

	mod, err := rt.InstantiateModule(ctx, compiled, cfg)
	if err != nil {
		rw.Fail("instantiate artifact: " + err.Error())
		return 1
	}

	_, err = mod.ExportedFunction("run").Call(ctx)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			rw.Fail(fmt.Sprintf("artifact `run` exceeded the %s hang guard", runnerHangGuard))
		} else {
			rw.Fail("artifact `run` trapped: " + err.Error())
		}
		return 1
	}
	if h.sawError {
		rw.Fail("host reported a rejected or failed operation during run")
		return 1
	}

	rw.Done()
	return 0
}

// checkImports is #519's second gate: a researcher module may import functions
// only from the `saucepan` host module (and only the six the runner exports)
// and from wasi_snapshot_preview1. Anything else — a stray `env`, a custom
// module, a saucepan function we do not provide — fails closed here, before
// the module is ever instantiated.
func checkImports(c wazero.CompiledModule) error {
	allowedSaucepan := make(map[string]bool, len(hostFuncNames))
	for _, n := range hostFuncNames {
		allowedSaucepan[n] = true
	}
	for _, fn := range c.ImportedFunctions() {
		mod, name, _ := fn.Import()
		switch mod {
		case "wasi_snapshot_preview1":
			// permitted wholesale — see runArtifact's ModuleConfig note
		case hostModuleName:
			if !allowedSaucepan[name] {
				return fmt.Errorf("artifact imports saucepan.%s, which the runner does not provide", name)
			}
		default:
			return fmt.Errorf("artifact imports %s.%s from outside the allowed modules (saucepan, wasi_snapshot_preview1)", mod, name)
		}
	}
	return nil
}
