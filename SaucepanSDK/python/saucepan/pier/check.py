"""Static allow-list checker for on-pier researcher code.

Mechanical, no human in the loop for v1. Given Python source, it rejects
anything outside a small allowed surface: **no imports, no subprocess, no
filesystem, no network, no introspection escapes** — only arithmetic, control
flow, local functions, a safe builtin subset, and calls to the injected
on-pier action API (:mod:`saucepan.pier.actions`).

It is an *allow-list* on calls and node types, plus a targeted deny on the
known sandbox-escape builtins and dunder attribute access. It runs in the SDK
before a campaign that carries pier code is submitted; the compiled wasm
module is separately checked for its imports at publish and on the pier.

Known limits:
- Method calls on a value (``x.foo()``) are allowed as long as ``foo`` is not
  underscore-prefixed — the checker cannot resolve the receiver's type. The
  wasm sandbox is the backstop: there is no filesystem/network/module to reach
  even if a method name slips through.
- It does not execute the code or evaluate constants; a determined obfuscation
  of a denied *name* via string tricks would still fail at wasm-compile time
  (no such symbol) or at import validation.
"""

from __future__ import annotations

import ast
from dataclasses import dataclass, field

from saucepan.pier.actions import ACTIONS

# Builtins a researcher may call. Pure, no I/O, no introspection.
_ALLOWED_BUILTINS: frozenset[str] = frozenset(
    {
        "abs", "all", "any", "bool", "dict", "divmod", "enumerate", "filter",
        "float", "int", "len", "list", "map", "max", "min", "pow", "print",
        "range", "reversed", "round", "set", "sorted", "str", "sum", "tuple",
        "zip", "isinstance", "abs", "complex", "bytes", "bytearray",
    }
)

# Names that must never appear (call or bare reference) — the classic escapes.
_DENIED_NAMES: frozenset[str] = frozenset(
    {
        "eval", "exec", "compile", "__import__", "open", "globals", "locals",
        "vars", "getattr", "setattr", "delattr", "input", "breakpoint",
        "help", "exit", "quit", "copyright", "credits", "license", "memoryview",
    }
)

# Statement/expression node types rejected outright.
_DENIED_NODES: tuple[type[ast.AST], ...] = (
    ast.Import,
    ast.ImportFrom,
    ast.Global,
    ast.Nonlocal,
    ast.With,
    ast.AsyncWith,
    ast.AsyncFor,
    ast.AsyncFunctionDef,
    ast.Await,
    ast.Yield,
    ast.YieldFrom,
    ast.ClassDef,
)

_DENIED_NODE_LABEL: dict[type[ast.AST], str] = {
    ast.Import: "import statement",
    ast.ImportFrom: "from-import statement",
    ast.Global: "global statement",
    ast.Nonlocal: "nonlocal statement",
    ast.With: "with statement (context managers can open files/sockets)",
    ast.AsyncWith: "async with",
    ast.AsyncFor: "async for",
    ast.AsyncFunctionDef: "async def",
    ast.Await: "await expression",
    ast.Yield: "yield expression",
    ast.YieldFrom: "yield from",
    ast.ClassDef: "class definition",
}


@dataclass(frozen=True)
class Violation:
    """One rejected construct, with a source location and a plain reason."""

    line: int
    col: int
    construct: str
    reason: str

    def __str__(self) -> str:  # pragma: no cover - trivial
        return f"line {self.line}:{self.col}: {self.construct} — {self.reason}"


@dataclass
class CheckResult:
    """Outcome of :func:`check_source`."""

    ok: bool
    violations: list[Violation] = field(default_factory=list)

    def raise_for_status(self) -> None:
        """Raise :class:`CheckError` if the code did not pass."""
        if not self.ok:
            raise CheckError(self.violations)


class CheckError(Exception):
    """Raised when researcher code fails the allow-list check."""

    def __init__(self, violations: list[Violation]) -> None:
        self.violations = violations
        joined = "\n  ".join(str(v) for v in violations)
        super().__init__(f"on-pier code rejected ({len(violations)} issue(s)):\n  {joined}")


class _Checker(ast.NodeVisitor):
    def __init__(self, allowed_call_names: frozenset[str]) -> None:
        self._allowed_calls = allowed_call_names
        self._local_funcs: set[str] = set()
        self.violations: list[Violation] = []

    def _flag(self, node: ast.AST, construct: str, reason: str) -> None:
        self.violations.append(
            Violation(
                line=getattr(node, "lineno", 0),
                col=getattr(node, "col_offset", 0),
                construct=construct,
                reason=reason,
            )
        )

    # --- collect top-level + nested function names first pass -------------
    def _collect_funcs(self, tree: ast.AST) -> None:
        for node in ast.walk(tree):
            if isinstance(node, ast.FunctionDef):
                self._local_funcs.add(node.name)

    # --- generic reject ------------------------------------------------------
    def generic_visit(self, node: ast.AST) -> None:
        if isinstance(node, _DENIED_NODES):
            label = _DENIED_NODE_LABEL.get(type(node), type(node).__name__)
            self._flag(node, label, "not allowed in on-pier code")
            return  # don't descend; one clear message per denied construct
        super().generic_visit(node)

    # --- attributes: block dunder / private access ------------------------
    def visit_Attribute(self, node: ast.Attribute) -> None:
        if node.attr.startswith("_"):
            self._flag(
                node,
                f"attribute access '.{node.attr}'",
                "underscore/dunder attributes are a sandbox-escape vector",
            )
        self.generic_visit(node)

    # --- names: block denied builtins even as bare references -------------
    def visit_Name(self, node: ast.Name) -> None:
        if isinstance(node.ctx, ast.Load) and node.id in _DENIED_NAMES:
            self._flag(node, f"name '{node.id}'", "denied builtin")
        self.generic_visit(node)

    # --- calls: allow-list the callable --------------------------------
    def visit_Call(self, node: ast.Call) -> None:
        func = node.func
        if isinstance(func, ast.Name):
            name = func.id
            if name in _DENIED_NAMES:
                self._flag(node, f"call to '{name}()'", "denied builtin")
            elif name not in self._allowed_calls and name not in self._local_funcs:
                self._flag(
                    node,
                    f"call to '{name}()'",
                    "not an allowed builtin, on-pier action, or a function defined in this file",
                )
        elif isinstance(func, ast.Attribute):
            # Method call on a value. The attribute visitor already rejects
            # underscore names; a plain method like list.append / str.format
            # is allowed — the wasm sandbox has nothing dangerous to reach.
            pass
        else:
            self._flag(node, "call", "callable must be a simple name or attribute")
        self.generic_visit(node)


def check_source(
    src: str,
    *,
    entrypoint: str = "run",
    extra_names: tuple[str, ...] = (),
) -> CheckResult:
    """Check *src* against the on-pier allow-list.

    ``entrypoint`` is the function the sandbox will call each frame; its
    absence is a violation. ``extra_names`` are additional callable names to
    permit (the injected numeric-helper surface, e.g. ``("frame", "log")``).
    """
    try:
        tree = ast.parse(src)
    except SyntaxError as exc:  # noqa: BLE001 - reported as a violation
        return CheckResult(
            ok=False,
            violations=[
                Violation(exc.lineno or 0, exc.offset or 0, "syntax error", str(exc.msg))
            ],
        )

    allowed = frozenset(_ALLOWED_BUILTINS | set(ACTIONS) | set(extra_names))
    checker = _Checker(allowed)
    checker._collect_funcs(tree)
    checker.visit(tree)

    violations = list(checker.violations)
    if not any(
        isinstance(n, ast.FunctionDef) and n.name == entrypoint for n in tree.body
    ):
        violations.append(
            Violation(0, 0, f"entrypoint '{entrypoint}'", "no top-level function with this name")
        )

    violations.sort(key=lambda v: (v.line, v.col))
    return CheckResult(ok=not violations, violations=violations)
