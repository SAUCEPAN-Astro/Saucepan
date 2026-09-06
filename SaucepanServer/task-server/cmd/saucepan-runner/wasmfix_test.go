package main

// Minimal hand-rolled WebAssembly encoder — enough to build the fixture
// modules the sandbox tests need without a wat2wasm/tinygo toolchain on the
// build machine. wazero validates every module on CompileModule, so a
// malformed fixture fails loudly in the test rather than silently.
//
// Everything here is i32-only (the one signature the runner's host functions
// and these fixtures use for parameters). Not general — do not grow it into a
// real assembler; if the compiler in #470 Stage 3 needs one, that is its own
// component.

func uleb(n uint32) []byte {
	var out []byte
	for {
		b := byte(n & 0x7f)
		n >>= 7
		if n != 0 {
			b |= 0x80
		}
		out = append(out, b)
		if n == 0 {
			return out
		}
	}
}

func sleb(n int32) []byte {
	var out []byte
	for {
		b := byte(n & 0x7f)
		n >>= 7
		signBit := b & 0x40
		if (n == 0 && signBit == 0) || (n == -1 && signBit != 0) {
			out = append(out, b)
			return out
		}
		out = append(out, b|0x80)
	}
}

func vec(items ...[]byte) []byte {
	out := uleb(uint32(len(items)))
	for _, it := range items {
		out = append(out, it...)
	}
	return out
}

func section(id byte, content []byte) []byte {
	out := []byte{id}
	out = append(out, uleb(uint32(len(content)))...)
	return append(out, content...)
}

func wasmName(s string) []byte {
	out := uleb(uint32(len(s)))
	return append(out, []byte(s)...)
}

// wasmImport describes one imported function (all params i32; results is 0 or 1).
type wasmImport struct {
	module, field string
	params        int
	results       int
}

// buildWasm assembles a module: the given function imports, one 1-page
// memory, an optional data blob at offset 0, and one defined function whose
// body is `body` (instruction bytes, no trailing 0x0b). Exports: "memory"
// always; "run" for the defined function unless exportRun is false.
func buildWasm(imports []wasmImport, data []byte, body []byte, exportRun bool) []byte {
	return buildWasmMem(imports, data, body, exportRun, 1)
}

// buildWasmMem is buildWasm with an explicit declared-minimum memory size
// (pages), for exercising modules with larger linear memories.
func buildWasmMem(imports []wasmImport, data []byte, body []byte, exportRun bool, minPages uint32) []byte {
	// --- type section: one functype per distinct import shape + the run type () -> ()
	// keep it simple: emit a type per import in order, then the run type last.
	var types [][]byte
	typeIdxForImport := make([]uint32, len(imports))
	for i, im := range imports {
		params := make([]byte, 0, im.params)
		for p := 0; p < im.params; p++ {
			params = append(params, 0x7f) // i32
		}
		results := []byte{}
		if im.results == 1 {
			results = []byte{0x7f}
		}
		ft := []byte{0x60}
		ft = append(ft, vec(splitBytes(params)...)...)
		ft = append(ft, vec(splitBytes(results)...)...)
		types = append(types, ft)
		typeIdxForImport[i] = uint32(i)
	}
	runType := []byte{0x60}
	runType = append(runType, vec()...) // no params
	runType = append(runType, vec()...) // no results
	types = append(types, runType)
	runTypeIdx := uint32(len(types) - 1)

	typeSec := section(1, vec(types...))

	// --- import section
	var imps [][]byte
	for i, im := range imports {
		e := wasmName(im.module)
		e = append(e, wasmName(im.field)...)
		e = append(e, 0x00) // importdesc: func
		e = append(e, uleb(typeIdxForImport[i])...)
		imps = append(imps, e)
	}
	importSec := section(2, vec(imps...))

	// --- function section: one defined function, type = run type
	funcSec := section(3, vec(uleb(runTypeIdx)))

	// --- memory section: one memory, given declared minimum
	memSec := section(5, vec(append([]byte{0x00}, uleb(minPages)...)))

	// --- export section
	definedFuncIdx := uint32(len(imports)) // imports first, then defined
	var exports [][]byte
	me := wasmName("memory")
	me = append(me, 0x02, 0x00) // exportdesc: mem 0
	exports = append(exports, me)
	if exportRun {
		re := wasmName("run")
		re = append(re, 0x00) // exportdesc: func
		re = append(re, uleb(definedFuncIdx)...)
		exports = append(exports, re)
	}
	exportSec := section(7, vec(exports...))

	// --- code section
	code := []byte{0x00} // vec(locals) = empty
	code = append(code, body...)
	code = append(code, 0x0b) // end
	codeEntry := append(uleb(uint32(len(code))), code...)
	codeSec := section(10, vec(codeEntry))

	// --- data section (active, mem 0, offset i32.const 0)
	var dataSec []byte
	if len(data) > 0 {
		off := []byte{0x41}
		off = append(off, sleb(0)...)
		off = append(off, 0x0b)
		seg := []byte{0x00} // active, memidx 0
		seg = append(seg, off...)
		seg = append(seg, uleb(uint32(len(data)))...)
		seg = append(seg, data...)
		dataSec = section(11, vec(seg))
	}

	out := []byte{0x00, 0x61, 0x73, 0x6d} // \0asm
	out = append(out, 0x01, 0x00, 0x00, 0x00)
	out = append(out, typeSec...)
	out = append(out, importSec...)
	out = append(out, funcSec...)
	out = append(out, memSec...)
	out = append(out, exportSec...)
	out = append(out, codeSec...)
	out = append(out, dataSec...)
	return out
}

// splitBytes turns a flat byte slice into a slice of 1-byte slices so vec()
// can length-prefix a value-type list (each entry is one byte).
func splitBytes(b []byte) [][]byte {
	out := make([][]byte, len(b))
	for i := range b {
		out[i] = []byte{b[i]}
	}
	return out
}

// emitCallBody returns a `run` body that pushes (offset=0, len=blobLen),
// calls import function 0 (must be the emit-shaped import), and drops its
// i32 result.
func emitCallBody(blobLen int) []byte {
	body := []byte{0x41}
	body = append(body, sleb(0)...) // i32.const 0
	body = append(body, 0x41)
	body = append(body, sleb(int32(blobLen))...) // i32.const blobLen
	body = append(body, 0x10, 0x00)              // call 0
	body = append(body, 0x1a)                    // drop
	return body
}

// noopBody returns an empty `run` body.
func noopBody() []byte { return nil }
