"""Compile a checked on-pier researcher routine (Python) to a wasm32 module
the pier runtime runs.

    from saucepan.pier import build

    wasm_bytes = build('''
        def run():
            mean = read_frame("mean")
            peak = read_frame("max")
            if mean > 5000 and peak > 60000:
                board_post("saturated frame")
                next_capture(exposure_sec=10)
    ''')

The compiled subset, v1 (deliberately small — it is a real compiler emitting
raw wasm, kept to what is easy to get right and test):

- one ``run()`` function, no parameters, no ``return``
- ``if / elif / else``, ``while``
- ``x = EXPR``, ``x += EXPR`` (all values are f64)
- arithmetic ``+ - * /``, unary ``-``, ``and / or / not``, one comparison
  per expression (``a < b``, chained comparisons are rejected)
- ``abs(x)``, ``min(a, b)``, ``max(a, b)``
- ``read_frame("<key>")`` -> f64  (key is a string literal; see fitsread.go
  for the set: mean/median/min/max/std/sum/width/height and ``hdr:<CARD>``)
- ``board_post("<literal>")``, ``log("<literal>")``
- ``next_capture(exposure_sec=<number literal>)``  (also ``gain=``/``filter=``)

Not yet supported (raises CompileError; tracked for Stage 3.1): f-strings /
dynamic message text, dynamic numbers in ``next_capture``, ``for`` loops,
helper ``def``, ``return``, ``board_read`` / ``list_piers`` result consumption,
carried state. The full-Python-subset compiler is future work; a campaign that
needs more can ship a ``wasm32-wasi`` module built in its own toolchain against
the documented host ABI (`shared/wire/piercode.go`, sandbox.go).
"""

from __future__ import annotations

import ast
import json

from saucepan.pier import _wasm
from saucepan.pier.check import CheckError, check_source

_ACTIONS_EMIT = {"board_post", "next_capture", "inbox_alert", "urgency_flag", "request_time"}

# Callables the compiled subset understands beyond the 8-action menu + the
# pure builtins the static checker already allows. `log` is a runtime host
# debug function, not a granted action.
_EXTRA_CALL_NAMES = ("log",)


class CompileError(Exception):
    """Raised when the source is checked-clean but outside the v1 compiled subset."""


class _Compiler:
    def __init__(self) -> None:
        self.e = _wasm.Emitter()
        self.locals: dict[str, int] = {}
        self.scratch: int | None = None
        self._pool = bytearray()
        self._interned: dict[bytes, tuple[int, int]] = {}

    # ── string pool ─────────────────────────────────────────────────────
    def intern(self, b: bytes) -> tuple[int, int]:
        if b in self._interned:
            return self._interned[b]
        off = len(self._pool)
        self._pool += b
        self._interned[b] = (off, len(b))
        return off, len(b)

    # ── locals ─────────────────────────────────────────────────────────
    def local_index(self, name: str) -> int:
        if name not in self.locals:
            self.locals[name] = len(self.locals)
        return self.locals[name]

    def scratch_index(self) -> int:
        if self.scratch is None:
            # scratch is always the last local
            self.scratch = len(self.locals)
            self.locals["$scratch"] = self.scratch
        return self.scratch

    # ── entry ──────────────────────────────────────────────────────────
    def compile_run(self, fn: ast.FunctionDef) -> None:
        if fn.args.args or fn.args.vararg or fn.args.kwarg or fn.args.kwonlyargs:
            raise CompileError("run() must take no parameters in v1")
        for stmt in fn.body:
            self.stmt(stmt)

    # ── statements ─────────────────────────────────────────────────────
    def stmt(self, node: ast.stmt) -> None:
        if isinstance(node, ast.Pass):
            return
        if isinstance(node, ast.Expr):
            self.expr(node.value)
            self.e.op("drop")  # discard the value the expression left on the stack
            return
        if isinstance(node, ast.Assign):
            if len(node.targets) != 1 or not isinstance(node.targets[0], ast.Name):
                raise CompileError("only `x = expr` (single name target) is supported")
            self.load_f64(node.value)
            self.e.local_set(self.local_index(node.targets[0].id))
            return
        if isinstance(node, ast.AugAssign):
            if not isinstance(node.target, ast.Name):
                raise CompileError("augmented assignment target must be a name")
            idx = self.local_index(node.target.id)
            self.e.local_get(idx)
            self.load_f64(node.value)
            self.e.op(_BINOP[type(node.op)])
            self.e.local_set(idx)
            return
        if isinstance(node, ast.If):
            self.load_bool_i32(node.test)
            self.e.if_(_wasm.VOID)
            for s in node.body:
                self.stmt(s)
            if node.orelse:
                self.e.else_()
                for s in node.orelse:
                    self.stmt(s)
            self.e.end()
            return
        if isinstance(node, ast.While):
            if node.orelse:
                raise CompileError("while ... else is not supported")
            self.e.block(_wasm.VOID)
            self.e.loop(_wasm.VOID)
            self.load_bool_i32(node.test)
            self.e.op("i32.eqz")
            self.e.br_if(1)  # condition false -> exit outer block
            for s in node.body:
                self.stmt(s)
            self.e.br(0)  # repeat loop
            self.e.end()  # loop
            self.e.end()  # block
            return
        raise CompileError(f"statement {type(node).__name__} is not in the v1 subset")

    # ── expressions ───────────────────────────────────────────────────
    def load_f64(self, node: ast.expr) -> None:
        """Emit code leaving one f64 on the stack (every subset value is f64)."""
        self.expr(node)

    def load_bool_i32(self, node: ast.expr) -> None:
        """Emit code leaving an i32 (0/1 truthiness) on the stack."""
        self.expr(node)
        self.e.f64_const(0.0)
        self.e.op("f64.ne")

    def expr(self, node: ast.expr) -> None:
        if isinstance(node, ast.Constant):
            if isinstance(node.value, bool):
                self.e.f64_const(1.0 if node.value else 0.0)
                return
            if isinstance(node.value, (int, float)):
                self.e.f64_const(float(node.value))
                return
            raise CompileError("string/other constants are only valid as call arguments")
        if isinstance(node, ast.Name):
            if node.id not in self.locals:
                raise CompileError(f"name {node.id!r} used before assignment")
            self.e.local_get(self.locals[node.id])
            return
        if isinstance(node, ast.UnaryOp):
            if isinstance(node.op, ast.USub):
                self.load_f64(node.operand)
                self.e.op("f64.neg")
                return
            if isinstance(node.op, ast.UAdd):
                self.load_f64(node.operand)
                return
            if isinstance(node.op, ast.Not):
                self.load_bool_i32(node.operand)
                self.e.op("i32.eqz")
                self.e.op("f64.convert_i32_s")
                return
            raise CompileError(f"unary {type(node.op).__name__} not supported")
        if isinstance(node, ast.BinOp):
            op = _BINOP.get(type(node.op))
            if op is None:
                raise CompileError(
                    f"operator {type(node.op).__name__} not supported in v1 (only + - * /)"
                )
            self.load_f64(node.left)
            self.load_f64(node.right)
            self.e.op(op)
            return
        if isinstance(node, ast.BoolOp):
            self._boolop(node)
            return
        if isinstance(node, ast.Compare):
            if len(node.ops) != 1:
                raise CompileError("chained comparisons (a < b < c) are not supported")
            self.load_f64(node.left)
            self.load_f64(node.comparators[0])
            op = type(node.ops[0])
            if op not in _CMP:
                raise CompileError(f"comparison {op.__name__} not supported")
            self.e.op(_CMP[op])  # -> i32
            self.e.op("f64.convert_i32_s")  # keep the value stack f64
            return
        if isinstance(node, ast.Call):
            self._call(node)
            return
        raise CompileError(f"expression {type(node).__name__} is not in the v1 subset")

    def _boolop(self, node: ast.BoolOp) -> None:
        # left-fold with short-circuit via an f64-typed if
        values = list(node.values)
        self.load_f64(values[0])
        for v in values[1:]:
            sb = self.scratch_index()
            self.e.local_tee(sb)  # keep left in scratch, also on stack
            self.e.f64_const(0.0)
            self.e.op("f64.ne")  # i32 truthiness of left
            self.e.if_(_wasm.F64)
            if isinstance(node.op, ast.And):
                self.load_f64(v)  # left truthy -> result is right
            else:  # Or
                self.e.local_get(sb)  # left truthy -> result is left
            self.e.else_()
            if isinstance(node.op, ast.And):
                self.e.local_get(sb)  # left falsy -> result is left
            else:
                self.load_f64(v)  # left falsy -> result is right
            self.e.end()

    def _call(self, node: ast.Call) -> None:
        if not isinstance(node.func, ast.Name):
            raise CompileError("only direct function calls are supported")
        name = node.func.id

        if name == "read_frame":
            key = _one_str_arg(node, "read_frame")
            off, length = self.intern(key.encode("utf-8"))
            self.e.i32_const(off)
            self.e.i32_const(length)
            self.e.call(_wasm.IMPORT_FRAME_STAT)  # -> f64
            return

        if name == "log":
            msg = _one_str_arg(node, "log")
            off, length = self.intern(msg.encode("utf-8"))
            self.e.i32_const(off)
            self.e.i32_const(length)
            self.e.call(_wasm.IMPORT_LOG)  # -> void
            self.e.f64_const(0.0)  # keep the value stack shape (Expr drops it)
            return

        if name in _ACTIONS_EMIT:
            record = self._emit_record(name, node)
            off, length = self.intern(record)
            self.e.i32_const(off)
            self.e.i32_const(length)
            self.e.call(_wasm.IMPORT_EMIT)  # -> i32 (Expr drops it)
            return

        if name in ("abs", "min", "max"):
            args = node.args
            if name == "abs":
                if len(args) != 1:
                    raise CompileError("abs() takes one argument")
                self.load_f64(args[0])
                self.e.op("f64.abs")
                return
            if len(args) != 2:
                raise CompileError(f"{name}() takes two arguments in v1")
            self.load_f64(args[0])
            self.load_f64(args[1])
            self.e.op("f64.min" if name == "min" else "f64.max")
            return

        raise CompileError(
            f"call to {name!r} is not a v1 action / builtin — allowed: read_frame, "
            "board_post, next_capture, inbox_alert, urgency_flag, request_time, log, "
            "abs, min, max"
        )

    def _emit_record(self, action: str, node: ast.Call) -> bytes:
        payload: dict = {}
        if action == "board_post" or action == "inbox_alert":
            msg = _one_str_arg(node, action)
            payload["message"] = msg
        elif action == "next_capture":
            if node.args:
                raise CompileError("next_capture() takes keyword arguments only")
            for kw in node.keywords:
                if kw.arg not in ("exposure_sec", "gain", "filter"):
                    raise CompileError(f"next_capture: unknown keyword {kw.arg!r}")
                if kw.arg == "filter":
                    if not (isinstance(kw.value, ast.Constant) and isinstance(kw.value.value, str)):
                        raise CompileError("next_capture(filter=...) must be a string literal")
                    payload["filter"] = kw.value.value
                else:
                    if not (
                        isinstance(kw.value, ast.Constant)
                        and isinstance(kw.value.value, (int, float))
                    ):
                        raise CompileError(
                            f"next_capture({kw.arg}=...) must be a number literal in v1"
                        )
                    payload[kw.arg] = float(kw.value.value)
            if not payload:
                raise CompileError("next_capture() needs at least one of exposure_sec/gain/filter")
        elif action == "request_time":
            if node.args:
                sec = node.args[0]
                if not (isinstance(sec, ast.Constant) and isinstance(sec.value, (int, float))):
                    raise CompileError("request_time(seconds) must be a number literal in v1")
                payload["seconds"] = float(sec.value)
        elif action == "urgency_flag":
            pass
        return json.dumps(
            {"action": action, "payload": payload}, separators=(",", ":")
        ).encode("utf-8")

    def finish(self) -> bytes:
        # scratch local is included in self.locals if it was used
        n_locals = len(self.locals)
        return _wasm.build_module(bytes(self.e.code), n_locals, bytes(self._pool))


# ── operator tables ───────────────────────────────────────────────────
_BINOP = {
    ast.Add: "f64.add",
    ast.Sub: "f64.sub",
    ast.Mult: "f64.mul",
    ast.Div: "f64.div",
}
_CMP = {
    ast.Lt: "f64.lt",
    ast.Gt: "f64.gt",
    ast.LtE: "f64.le",
    ast.GtE: "f64.ge",
    ast.Eq: "f64.eq",
    ast.NotEq: "f64.ne",
}


def _one_str_arg(node: ast.Call, fn: str) -> str:
    if len(node.args) != 1 or node.keywords:
        raise CompileError(f"{fn}() takes exactly one string-literal argument in v1")
    a = node.args[0]
    if not (isinstance(a, ast.Constant) and isinstance(a.value, str)):
        raise CompileError(f"{fn}() argument must be a string literal in v1 (no f-strings yet)")
    return a.value


def build(src: str, *, skip_check: bool = False) -> bytes:
    """Check `src` (unless `skip_check`) then compile it to wasm bytes.

    Raises :class:`~saucepan.pier.check.CheckError` if the source fails the
    static allow-list, or :class:`CompileError` if it is clean but uses a
    construct outside the v1 compiled subset.
    """
    if not skip_check:
        result = check_source(src, extra_names=_EXTRA_CALL_NAMES)
        if not result.ok:
            raise CheckError(result.violations)

    tree = ast.parse(src)
    run_fn = next(
        (n for n in tree.body if isinstance(n, ast.FunctionDef) and n.name == "run"), None
    )
    if run_fn is None:
        raise CompileError("no top-level `def run():`")
    for n in tree.body:
        if isinstance(n, ast.FunctionDef) and n.name != "run":
            raise CompileError(
                "helper functions are not supported in v1 — inline the logic into run()"
            )
        if not isinstance(n, ast.FunctionDef):
            raise CompileError("only a single `def run():` is allowed at module level in v1")

    c = _Compiler()
    c.compile_run(run_fn)
    return c.finish()
