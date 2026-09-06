"""Unit tests for metrics_hook.py — the datalake<->metrics-sidecar adapter.

Focus: the pure _night_id helper, and the fail-open contract on
notify_upload_complete / notify_stack_complete (must never raise, even
when the metrics package is unavailable or dispatch blows up).
"""

from __future__ import annotations

import builtins
from unittest.mock import patch

import pytest

import metrics_hook


def test_night_id_none_without_telescope_id():
    assert metrics_hook._night_id(None, "2026-08-24T00:00:00") is None


def test_night_id_combines_telescope_and_date():
    assert metrics_hook._night_id("node1", "2026-08-24T12:34:56") == "node1_2026-08-24"


def test_night_id_defaults_to_today_when_no_date_obs():
    result = metrics_hook._night_id("node1", None)
    assert result.startswith("node1_")
    assert len(result) == len("node1_2026-08-24")


def test_notify_upload_complete_disabled_via_env_is_noop(monkeypatch):
    monkeypatch.setenv("METRICS_SIDECAR", "0")
    metrics_hook.notify_upload_complete("upload-1")  # must not raise


def test_notify_upload_complete_missing_metrics_package_is_noop(monkeypatch):
    monkeypatch.setenv("METRICS_SIDECAR", "1")
    real_import = builtins.__import__

    def fake_import(name, *args, **kwargs):
        if name == "metrics.sidecar" or name.startswith("metrics.sidecar"):
            raise ImportError("no metrics package")
        return real_import(name, *args, **kwargs)

    with patch("builtins.__import__", side_effect=fake_import):
        metrics_hook.notify_upload_complete("upload-1")  # must not raise


def test_notify_upload_complete_no_context_is_noop(monkeypatch):
    monkeypatch.setenv("METRICS_SIDECAR", "1")
    with (
        patch("metrics_hook._build_context", return_value=None),
        patch(
            "sys.modules",
            {
                **__import__("sys").modules,
                "metrics.sidecar": __import__("types").ModuleType("metrics.sidecar"),
            },
        ),
    ):
        metrics_hook.notify_upload_complete("does-not-exist")  # must not raise


def test_notify_upload_complete_dispatch_failure_is_fail_open(monkeypatch):
    monkeypatch.setenv("METRICS_SIDECAR", "1")
    fake_sidecar = __import__("types").ModuleType("metrics.sidecar")
    fake_sidecar.dispatch = lambda ctx, save_fn: (_ for _ in ()).throw(RuntimeError("boom"))

    with (
        patch("metrics_hook._build_context", return_value={"upload_id": "u1"}),
        patch.dict("sys.modules", {"metrics.sidecar": fake_sidecar}),
    ):
        metrics_hook.notify_upload_complete("u1")  # must not raise despite dispatch blowing up


def test_notify_stack_complete_disabled_via_env_is_noop(monkeypatch):
    monkeypatch.setenv("METRICS_SIDECAR", "0")
    metrics_hook.notify_stack_complete({}, "/out.fits")  # must not raise


def test_notify_stack_complete_missing_metrics_package_is_noop(monkeypatch):
    monkeypatch.setenv("METRICS_SIDECAR", "1")
    real_import = builtins.__import__

    def fake_import(name, *args, **kwargs):
        if name == "metrics.sidecar" or name.startswith("metrics.sidecar"):
            raise ImportError("no metrics package")
        return real_import(name, *args, **kwargs)

    with patch("builtins.__import__", side_effect=fake_import):
        metrics_hook.notify_stack_complete({"stack_snr": 5.0}, "/out.fits")  # must not raise


def test_notify_stack_complete_dispatch_failure_is_fail_open(monkeypatch):
    monkeypatch.setenv("METRICS_SIDECAR", "1")
    fake_sidecar = __import__("types").ModuleType("metrics.sidecar")
    fake_sidecar.dispatch_stack = lambda ctx, save_fn: (_ for _ in ()).throw(RuntimeError("boom"))

    with patch.dict("sys.modules", {"metrics.sidecar": fake_sidecar}):
        metrics_hook.notify_stack_complete({}, "/out.fits")  # must not raise


def test_build_context_returns_none_for_unknown_upload(tmp_path, monkeypatch):
    db_path = tmp_path / "catalog.db"
    monkeypatch.setenv("DATABASE_URL", f"sqlite:///{db_path}")
    import db as db_mod

    db_mod._engine = None
    db_mod._SessionLocal = None
    db_mod.init_db()
    try:
        assert metrics_hook._build_context("does-not-exist") is None
    finally:
        db_mod._engine = None
        db_mod._SessionLocal = None


def test_build_context_does_not_forward_identity_metadata(tmp_path, monkeypatch):
    db_path = tmp_path / "catalog.db"
    monkeypatch.setenv("DATABASE_URL", f"sqlite:///{db_path}")
    import db as db_mod
    from catalog import Frame, Upload
    from db import session_scope

    db_mod._engine = None
    db_mod._SessionLocal = None
    db_mod.init_db()
    try:
        with session_scope() as session:
            session.add(
                Upload(
                    id="u-private",
                    status="completed",
                    bucket="b",
                    object_key="camp/frame.fits",
                    filename="frame.fits",
                    campaign_id="camp",
                    metadata_json={
                        "observer": "private@example.invalid",
                        "user_id": "private-user-id",
                    },
                )
            )
            session.add(
                Frame(
                    id="f-private",
                    upload_id="u-private",
                    campaign_id="camp",
                    object_key="camp/frame.fits",
                )
            )
        ctx = metrics_hook._build_context("u-private")
        assert ctx is not None
        assert "observer" not in ctx
        assert "user_id" not in ctx
    finally:
        db_mod._engine = None
        db_mod._SessionLocal = None
