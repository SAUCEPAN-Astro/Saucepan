"""A tiny WebAssembly binary encoder — just enough for :mod:`saucepan.pier.build`
to emit the module the pier runtime (`cmd/saucepan-runner`) runs.

Not a general assembler. One memory, three imported host functions
(`saucepan.frame_stat`, `saucepan.emit`, `saucepan.log`), one exported `run`
function, one data segment (the string/JSON pool). Every value the compiler
puts on the wasm stack is `f64`; control flow converts to `i32` locally.

The runtime (`sandbox.go` `checkImports`) rejects a module that imports anything
outside the `saucepan.*` names below + `wasi_snapshot_preview1`, so this encoder
deliberately cannot produce one that would pass but do more.
"""

from __future__ import annotations

import struct

# value types
I32 = 0x7F
F64 = 0x7C
# blocktype
VOID = 0x40

# opcodes used by the compiler
OP = {
    "unreachable": 0x00,
    "block": 0x02,
    "loop": 0x03,
    "if": 0x04,
    "else": 0x05,
    "end": 0x0B,
    "br": 0x0C,
    "br_if": 0x0D,
    "call": 0x10,
    "drop": 0x1A,
    "local.get": 0x20,
    "local.set": 0x21,
    "local.tee": 0x22,
    "i32.const": 0x41,
    "f64.const": 0x44,
    "i32.eqz": 0x45,
    "f64.eq": 0x61,
    "f64.ne": 0x62,
    "f64.lt": 0x63,
    "f64.gt": 0x64,
    "f64.le": 0x65,
    "f64.ge": 0x66,
    "f64.abs": 0x99,
    "f64.neg": 0x9A,
    "f64.ceil": 0x9B,
    "f64.floor": 0x9C,
    "f64.trunc": 0x9D,
    "f64.nearest": 0x9E,
    "f64.sqrt": 0x9F,
    "f64.add": 0xA0,
    "f64.sub": 0xA1,
    "f64.mul": 0xA2,
    "f64.div": 0xA3,
    "f64.min": 0xA4,
    "f64.max": 0xA5,
    "f64.convert_i32_s": 0xB7,
    "i32.trunc_f64_s": 0xAA,
}


def uleb(n: int) -> bytes:
    if n < 0:
        raise ValueError("uleb of negative")
    out = bytearray()
    while True:
        b = n & 0x7F
        n >>= 7
        if n:
            out.append(b | 0x80)
        else:
            out.append(b)
            return bytes(out)


def sleb(n: int) -> bytes:
    out = bytearray()
    while True:
        b = n & 0x7F
        n >>= 7
        if (n == 0 and not (b & 0x40)) or (n == -1 and (b & 0x40)):
            out.append(b)
            return bytes(out)
        out.append(b | 0x80)


def _vec(items: list[bytes]) -> bytes:
    return uleb(len(items)) + b"".join(items)


def _section(sid: int, body: bytes) -> bytes:
    return bytes([sid]) + uleb(len(body)) + body


def _name(s: str) -> bytes:
    raw = s.encode("utf-8")
    return uleb(len(raw)) + raw


class Emitter:
    """Accumulates instruction bytes for the `run` function body."""

    def __init__(self) -> None:
        self.code = bytearray()

    def op(self, name: str) -> None:
        self.code.append(OP[name])

    def f64_const(self, v: float) -> None:
        self.code.append(OP["f64.const"])
        self.code += struct.pack("<d", float(v))

    def i32_const(self, v: int) -> None:
        self.code.append(OP["i32.const"])
        self.code += sleb(v)

    def local_get(self, idx: int) -> None:
        self.code.append(OP["local.get"])
        self.code += uleb(idx)

    def local_set(self, idx: int) -> None:
        self.code.append(OP["local.set"])
        self.code += uleb(idx)

    def local_tee(self, idx: int) -> None:
        self.code.append(OP["local.tee"])
        self.code += uleb(idx)

    def call(self, funcidx: int) -> None:
        self.code.append(OP["call"])
        self.code += uleb(funcidx)

    def block(self, blocktype: int = VOID) -> None:
        self.code.append(OP["block"])
        self.code.append(blocktype)

    def loop(self, blocktype: int = VOID) -> None:
        self.code.append(OP["loop"])
        self.code.append(blocktype)

    def if_(self, blocktype: int = VOID) -> None:
        self.code.append(OP["if"])
        self.code.append(blocktype)

    def else_(self) -> None:
        self.code.append(OP["else"])

    def end(self) -> None:
        self.code.append(OP["end"])

    def br(self, depth: int) -> None:
        self.code.append(OP["br"])
        self.code += uleb(depth)

    def br_if(self, depth: int) -> None:
        self.code.append(OP["br_if"])
        self.code += uleb(depth)


# Import indices are fixed: the compiler and this module agree on them.
IMPORT_FRAME_STAT = 0  # (i32 i32) -> f64
IMPORT_EMIT = 1  # (i32 i32) -> i32
IMPORT_LOG = 2  # (i32 i32) -> ()
FIRST_DEFINED_FUNC = 3  # `run`


def build_module(run_body: bytes, n_f64_locals: int, data_blob: bytes) -> bytes:
    """Assemble the final module.

    `run_body` is the encoded instruction stream for `run` (no trailing `end`);
    `n_f64_locals` is how many f64 locals it uses; `data_blob` is the string
    pool placed at memory offset 0.
    """
    # ── type section ──────────────────────────────────────────────────────
    t_i32i32_f64 = bytes([0x60]) + _vec([bytes([I32]), bytes([I32])]) + _vec([bytes([F64])])
    t_i32i32_i32 = bytes([0x60]) + _vec([bytes([I32]), bytes([I32])]) + _vec([bytes([I32])])
    t_i32i32_void = bytes([0x60]) + _vec([bytes([I32]), bytes([I32])]) + _vec([])
    t_void_void = bytes([0x60]) + _vec([]) + _vec([])
    type_sec = _section(1, _vec([t_i32i32_f64, t_i32i32_i32, t_i32i32_void, t_void_void]))

    # ── import section ───────────────────────────────────────────────────
    def imp(field: str, typeidx: int) -> bytes:
        return _name("saucepan") + _name(field) + bytes([0x00]) + uleb(typeidx)

    import_sec = _section(2, _vec([imp("frame_stat", 0), imp("emit", 1), imp("log", 2)]))

    # ── function section (one defined func: run, type 3) ─────────────────
    func_sec = _section(3, _vec([uleb(3)]))

    # ── memory section (1 page min) ─────────────────────────────────────
    mem_sec = _section(5, _vec([bytes([0x00]) + uleb(1)]))

    # ── export section ─────────────────────────────────────────────────
    exp_mem = _name("memory") + bytes([0x02, 0x00])
    exp_run = _name("run") + bytes([0x00]) + uleb(FIRST_DEFINED_FUNC)
    export_sec = _section(7, _vec([exp_mem, exp_run]))

    # ── code section ───────────────────────────────────────────────────
    locals_decl = _vec([uleb(n_f64_locals) + bytes([F64])]) if n_f64_locals else _vec([])
    func_body = locals_decl + bytes(run_body) + bytes([OP["end"]])
    code_sec = _section(10, _vec([uleb(len(func_body)) + func_body]))

    # ── data section ───────────────────────────────────────────────────
    data_sec = b""
    if data_blob:
        offset_expr = bytes([OP["i32.const"]]) + sleb(0) + bytes([OP["end"]])
        seg = bytes([0x00]) + offset_expr + uleb(len(data_blob)) + data_blob
        data_sec = _section(11, _vec([seg]))

    return (
        b"\x00asm\x01\x00\x00\x00"
        + type_sec
        + import_sec
        + func_sec
        + mem_sec
        + export_sec
        + code_sec
        + data_sec
    )
