"""saucepan.pier.build — Python-subset to wasm32 compiler for on-pier code."""

from __future__ import annotations

import struct

import pytest

from saucepan.pier import build
from saucepan.pier.build import CompileError
from saucepan.pier.check import CheckError

WASM_MAGIC = b"\x00asm\x01\x00\x00\x00"


def _sections(mod: bytes) -> dict[int, bytes]:
    """Split a wasm module into {section_id: body} (skips custom sections)."""
    assert mod[:8] == WASM_MAGIC
    i = 8
    out: dict[int, bytes] = {}
    while i < len(mod):
        sid = mod[i]
        i += 1
        size, i = _uleb(mod, i)
        out[sid] = mod[i : i + size]
        i += size
    return out


def _uleb(buf: bytes, i: int) -> tuple[int, int]:
    val = 0
    shift = 0
    while True:
        b = buf[i]
        i += 1
        val |= (b & 0x7F) << shift
        if not (b & 0x80):
            return val, i
        shift += 7


def test_minimal_module_is_valid_wasm():
    mod = build("def run():\n    pass\n")
    assert mod[:4] == b"\x00asm"
    assert mod[4:8] == b"\x01\x00\x00\x00"
    secs = _sections(mod)
    # type(1) import(2) func(3) memory(5) export(7) code(10) are always present
    for sid in (1, 2, 3, 5, 7, 10):
        assert sid in secs, f"section {sid} missing"


def test_imports_are_only_the_three_host_funcs():
    mod = build('def run():\n    x = read_frame("mean")\n    board_post("hi")\n')
    import_body = _sections(mod)[2]
    # every import names module "saucepan"
    assert import_body.count(b"saucepan") == 3
    for field in (b"frame_stat", b"emit", b"log"):
        assert field in import_body
    # nothing outside that set
    assert b"wasi" not in import_body
    assert b"env" not in import_body


def test_board_post_message_lands_in_the_data_pool():
    mod = build('def run():\n    board_post("saturated frame")\n')
    data_body = _sections(mod)[11]
    assert b'"action":"board_post"' in data_body
    assert b'"message":"saturated frame"' in data_body


def test_next_capture_number_literal_is_serialised():
    mod = build("def run():\n    next_capture(exposure_sec=10)\n")
    data_body = _sections(mod)[11]
    assert b'"action":"next_capture"' in data_body
    assert b'"exposure_sec":10' in data_body


def test_data_pool_interns_repeated_strings_once():
    mod = build(
        'def run():\n'
        '    a = read_frame("mean")\n'
        '    b = read_frame("mean")\n'
        '    c = read_frame("mean")\n'
    )
    data_body = _sections(mod)[11]
    # "mean" appears once in the pool even though read_frame("mean") is called 3x
    assert data_body.count(b"mean") == 1


def test_float_literals_encode_as_f64_const():
    # 5000.0 little-endian double should appear verbatim in the code section
    mod = build("def run():\n    x = 5000\n    if x > 1:\n        x = x + 1\n")
    code_body = _sections(mod)[10]
    assert struct.pack("<d", 5000.0) in code_body


@pytest.mark.parametrize(
    "src",
    [
        'def run():\n    board_post(f"dynamic {read_frame(\'mean\')}")\n',  # f-string
        "def run():\n    for i in range(3):\n        pass\n",  # for loop
        "def run():\n    return 1\n",  # return
        "def run():\n    def helper():\n        pass\n    helper()\n",  # helper def
        "def run():\n    x = 1 % 2\n",  # unsupported operator
        "def run():\n    x = 1 < 2 < 3\n",  # chained comparison
        "def run():\n    next_capture(exposure_sec=read_frame('mean'))\n",  # dynamic number
    ],
)
def test_out_of_subset_constructs_raise_compile_error(src):
    with pytest.raises(CompileError):
        build(src)


def test_static_checker_still_runs():
    # `open` is a denied builtin — must be caught before compilation
    with pytest.raises(CheckError):
        build('def run():\n    open("/etc/passwd")\n')


def test_skip_check_still_compiles_clean_subset():
    mod = build("def run():\n    pass\n", skip_check=True)
    assert mod[:4] == b"\x00asm"


def test_missing_run_is_rejected():
    # the static checker catches a missing entrypoint before the compiler does
    with pytest.raises((CompileError, CheckError)):
        build("def other():\n    pass\n")
