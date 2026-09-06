"""Tests for metrics.cli — subprocess sidecar entrypoints."""

from __future__ import annotations

import json
import sys

import pytest
from metrics.cli import _load_emit_context, main

# ---------------------------------------------------------------------------
# _load_emit_context
# ---------------------------------------------------------------------------


def test_load_emit_context_from_context_json_file(tmp_path):
    path = tmp_path / "ctx.json"
    path.write_text(json.dumps({"telescope_id": "T1"}), encoding="utf-8")
    ctx = _load_emit_context("u1", str(path))
    assert ctx["telescope_id"] == "T1"
    assert ctx["upload_id"] == "u1"  # setdefault applied


def test_load_emit_context_removes_subprocess_temp_file(tmp_path, monkeypatch):
    path = tmp_path / "ctx.json"
    path.write_text(json.dumps({"telescope_id": "T1"}), encoding="utf-8")
    monkeypatch.setenv("METRICS_CONTEXT_TEMP_FILE", "1")
    ctx = _load_emit_context("u1", str(path))
    assert ctx["telescope_id"] == "T1"
    assert not path.exists()


def test_load_emit_context_context_json_does_not_override_existing_upload_id(tmp_path):
    path = tmp_path / "ctx.json"
    path.write_text(json.dumps({"upload_id": "explicit"}), encoding="utf-8")
    ctx = _load_emit_context("u1", str(path))
    assert ctx["upload_id"] == "explicit"


def test_load_emit_context_from_stdin(monkeypatch):
    monkeypatch.setattr(sys, "stdin", _FakeStdin(json.dumps({"telescope_id": "T2"})))
    ctx = _load_emit_context("u2", "-")
    assert ctx["telescope_id"] == "T2"


def test_load_emit_context_from_env_var_file(tmp_path, monkeypatch):
    path = tmp_path / "ctx.json"
    path.write_text(json.dumps({"telescope_id": "T3"}), encoding="utf-8")
    monkeypatch.setenv("METRICS_CONTEXT_JSON", str(path))
    ctx = _load_emit_context("u3", None)
    assert ctx["telescope_id"] == "T3"


def test_load_emit_context_from_env_var_inline_json(monkeypatch):
    monkeypatch.setenv("METRICS_CONTEXT_JSON", json.dumps({"telescope_id": "T4"}))
    ctx = _load_emit_context("u4", None)
    assert ctx["telescope_id"] == "T4"


def test_load_emit_context_from_env_var_stdin_marker(monkeypatch):
    monkeypatch.setenv("METRICS_CONTEXT_JSON", "-")
    monkeypatch.setattr(sys, "stdin", _FakeStdin(json.dumps({"telescope_id": "T5"})))
    ctx = _load_emit_context("u5", None)
    assert ctx["telescope_id"] == "T5"


def test_load_emit_context_non_dict_json_returns_none(tmp_path):
    path = tmp_path / "ctx.json"
    path.write_text(json.dumps([1, 2, 3]), encoding="utf-8")
    assert _load_emit_context("u1", str(path)) is None


def test_load_emit_context_no_source_and_hook_unimportable_returns_none(monkeypatch):
    monkeypatch.delenv("METRICS_CONTEXT_JSON", raising=False)
    # Force `import metrics_hook` to raise ImportError deterministically,
    # independent of whatever else has touched sys.path in this session.
    monkeypatch.setitem(sys.modules, "metrics_hook", None)
    assert _load_emit_context("u1", None) is None


def test_load_emit_context_prefers_datalake_hook_when_importable(monkeypatch):
    monkeypatch.delenv("METRICS_CONTEXT_JSON", raising=False)

    class _FakeHook:
        @staticmethod
        def _build_context(upload_id):
            return {"upload_id": upload_id, "from_hook": True}

    monkeypatch.setitem(sys.modules, "metrics_hook", _FakeHook)
    ctx = _load_emit_context("u9", None)
    assert ctx == {"upload_id": "u9", "from_hook": True}


class _FakeStdin:
    def __init__(self, text: str) -> None:
        self._text = text

    def read(self) -> str:
        return self._text


# ---------------------------------------------------------------------------
# main() subcommands
# ---------------------------------------------------------------------------


def test_main_registry_stats_prints_json(monkeypatch, capsys):
    monkeypatch.setattr(sys, "argv", ["metrics-sidecar", "registry-stats"])
    main()
    out = json.loads(capsys.readouterr().out)
    assert set(out.keys()) == {"live", "wait", "total"}
    assert out["total"] == out["live"] + out["wait"]


def test_main_emit_upload_missing_context_exits_nonzero(monkeypatch, capsys):
    monkeypatch.delenv("METRICS_CONTEXT_JSON", raising=False)
    monkeypatch.setitem(sys.modules, "metrics_hook", None)
    monkeypatch.setattr(sys, "argv", ["metrics-sidecar", "emit-upload", "u1"])
    with pytest.raises(SystemExit) as exc:
        main()
    assert exc.value.code == 1
    err = json.loads(capsys.readouterr().err)
    assert "error" in err


def test_main_emit_upload_with_context_json_runs_dispatch(tmp_path, monkeypatch, capsys):
    path = tmp_path / "ctx.json"
    path.write_text(json.dumps({"upload_id": "u1"}), encoding="utf-8")
    monkeypatch.setattr(
        sys, "argv", ["metrics-sidecar", "emit-upload", "u1", "--context-json", str(path)]
    )
    main()
    # _save prints each observation as its own JSON line (metrics_store is not
    # importable outside datalake), then main() prints a final {"ok": true, ...}
    # line — only the last line is the emit-upload command's own output.
    lines = [ln for ln in capsys.readouterr().out.splitlines() if ln.strip()]
    assert json.loads(lines[-1]) == {"ok": True, "upload_id": "u1"}


def test_main_rollup_session_with_frames_json(tmp_path, monkeypatch, capsys):
    frames_path = tmp_path / "frames.json"
    frames_path.write_text(json.dumps([{"zp": 22.0}, {"zp": 22.0}]), encoding="utf-8")
    monkeypatch.setattr(
        sys,
        "argv",
        [
            "metrics-sidecar",
            "rollup-session",
            "--telescope-id",
            "T1",
            "--night-id",
            "T1_2026-01-01",
            "--frames-json",
            str(frames_path),
        ],
    )
    main()
    out = json.loads(capsys.readouterr().out)
    assert out["observation"]["metrics"]["session.frames"] == 2
    assert "slo_events" in out


def test_main_rollup_session_frames_json_must_be_array(tmp_path, monkeypatch, capsys):
    frames_path = tmp_path / "frames.json"
    frames_path.write_text(json.dumps({"not": "a list"}), encoding="utf-8")
    monkeypatch.setattr(
        sys,
        "argv",
        [
            "metrics-sidecar",
            "rollup-session",
            "--telescope-id",
            "T1",
            "--night-id",
            "T1_2026-01-01",
            "--frames-json",
            str(frames_path),
        ],
    )
    with pytest.raises(SystemExit) as exc:
        main()
    assert exc.value.code == 1
    err = json.loads(capsys.readouterr().err)
    assert "error" in err
