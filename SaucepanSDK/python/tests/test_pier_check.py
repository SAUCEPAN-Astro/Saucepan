"""Corpus test for the on-pier code allow-list checker."""

from __future__ import annotations

import pytest

from saucepan.pier import check_source
from saucepan.pier.check import CheckError

# Names the sandbox injects besides the 8 actions — the frame handle + a log fn.
EXTRA = ("frame", "log", "state")


# --------------------------------------------------------------------------
# Must be REJECTED
# --------------------------------------------------------------------------
REJECTED: dict[str, str] = {
    "import os": """
import os
def run(frame):
    return None
""",
    "from subprocess import run": """
from subprocess import run as _r
def run(frame):
    _r(["ls"])
""",
    "__import__ escape": """
def run(frame):
    m = __import__("os")
    return m
""",
    "eval": """
def run(frame):
    return eval("2 + 2")
""",
    "exec": """
def run(frame):
    exec("x = 1")
""",
    "open a file": """
def run(frame):
    return open("/etc/passwd").read()
""",
    "dunder subclass walk": """
def run(frame):
    return ().__class__.__bases__[0].__subclasses__()
""",
    "getattr introspection": """
def run(frame):
    return getattr(frame, "__class__")
""",
    "with statement": """
def run(frame):
    with frame as f:
        return f
""",
    "async def entrypoint": """
async def run(frame):
    return None
""",
    "globals()": """
def run(frame):
    return globals()
""",
    "aliased denied builtin as bare name": """
def run(frame):
    f = eval
    return f("1")
""",
    "call to undefined function": """
def run(frame):
    return mystery_helper(frame)
""",
    "no run entrypoint": """
def helper(frame):
    return frame
""",
    "yield / generator": """
def run(frame):
    yield frame
""",
    "class definition": """
class Sneaky:
    pass
def run(frame):
    return Sneaky()
""",
}


# --------------------------------------------------------------------------
# Must PASS
# --------------------------------------------------------------------------
ACCEPTED: dict[str, str] = {
    "minimal read + board": """
def run(frame):
    px = read_frame()
    if px is not None:
        board_post("frame seen")
    return None
""",
    "arithmetic + comprehension + local helper": """
def _mean(xs):
    return sum(xs) / max(len(xs), 1)

def run(frame):
    rows = read_frame()
    vals = [v * 2 for v in range(10) if v % 2 == 0]
    m = _mean(vals)
    if m > 5:
        board_post("bright")
    return m
""",
    "next_capture with dict + bounds math": """
def run(frame):
    cur = read_frame()
    new_exp = min(max(30.0, 12.5), 300.0)
    next_capture({"exposure_sec": new_exp, "filter": "R"})
""",
    "fstrings, list methods, sorted": """
def run(frame):
    notes = []
    for i in range(3):
        notes.append(f"tile {i}")
    board_post(", ".join(sorted(notes)))
""",
    "control flow + injected names": """
def run(frame):
    log("start")
    seen = state or 0
    while seen < 3:
        seen = seen + 1
    if seen >= 3:
        inbox_alert("done")
    return {"seen": seen}
""",
}


@pytest.mark.parametrize("name", list(REJECTED))
def test_rejected(name: str) -> None:
    result = check_source(REJECTED[name], extra_names=EXTRA)
    assert not result.ok, f"{name!r} should have been rejected"
    assert result.violations
    with pytest.raises(CheckError):
        result.raise_for_status()


@pytest.mark.parametrize("name", list(ACCEPTED))
def test_accepted(name: str) -> None:
    result = check_source(ACCEPTED[name], extra_names=EXTRA)
    assert result.ok, f"{name!r} should pass, got: {[str(v) for v in result.violations]}"
    result.raise_for_status()  # must not raise


def test_syntax_error_is_a_violation_not_a_crash() -> None:
    result = check_source("def run(frame):\n    return (", extra_names=EXTRA)
    assert not result.ok
    assert result.violations[0].construct == "syntax error"


def test_violation_message_names_the_construct() -> None:
    result = check_source("import os\ndef run(frame):\n    return None\n")
    msg = str(result.violations[0])
    assert "import" in msg and "line 1" in msg
