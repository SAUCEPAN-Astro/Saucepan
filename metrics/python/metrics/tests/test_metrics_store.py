"""Tests for metrics_store.py — the datalake SQLite persistence adapter.

metrics_store.py lives at metrics/python/metrics_store.py (sibling of the
`metrics` package) and is the storage "plug" datalake's db.init_db() wires
up. It needs datalake's `db`/`catalog` modules, which aren't part of this
package's own dependency closure, so this test adds the datalake directory
to sys.path (via monkeypatch, auto-reverted) and boots a throwaway sqlite
file DB per test — mirroring the pattern in
SaucepanServer/storage-server/datalake/tests/test_upload_auth.py.
"""

from __future__ import annotations

import json
import pathlib
import sys

import pytest

REPO_ROOT = pathlib.Path(__file__).resolve().parents[4]
DATALAKE_DIR = REPO_ROOT / "SaucepanServer" / "storage-server" / "datalake"
METRICS_PYTHON_DIR = pathlib.Path(__file__).resolve().parents[2]

pytestmark = pytest.mark.skipif(
    not (DATALAKE_DIR / "db.py").is_file(),
    reason="datalake service tree not present in this checkout",
)


@pytest.fixture()
def store(tmp_path, monkeypatch):
    """Boot a throwaway sqlite-backed metrics_store bound to a temp DB file."""
    # Prepend both dirs via monkeypatch (auto-reverted) so db.py's own
    # unconditional sys.path insert of metrics/python becomes a no-op —
    # otherwise that insert bypasses monkeypatch's cleanup and metrics_store
    # stays importable (with a stale/default DATABASE_URL) for every test
    # that runs later in this session.
    monkeypatch.syspath_prepend(str(DATALAKE_DIR))
    monkeypatch.syspath_prepend(str(METRICS_PYTHON_DIR))
    for name in ("db", "metrics_store", "catalog"):
        sys.modules.pop(name, None)
    db_path = tmp_path / "metrics_store_test.db"
    monkeypatch.setenv("DATABASE_URL", f"sqlite:///{db_path}")

    import db as db_mod

    db_mod._engine = None
    db_mod._SessionLocal = None
    db_mod.init_db()

    import metrics_store as ms

    yield ms

    db_mod._engine = None
    db_mod._SessionLocal = None
    # Drop the module-level references so a later test's monkeypatched
    # sys.path/env doesn't silently reuse a stale cached engine.
    for name in ("db", "metrics_store", "catalog"):
        sys.modules.pop(name, None)


def _observation(*, upload_id="u1", telescope_id="T1", frame_id=None, metrics=None, wait_pile=None):
    return {
        "observation_id": None,
        "producer": "frame_headers",
        "context": {
            "upload_id": upload_id,
            "telescope_id": telescope_id,
            "frame_id": frame_id,
            "node_id": "NODE_1",
        },
        "metrics": metrics or {"frame.zp": 22.0},
        "wait_pile": wait_pile or [],
    }


def test_save_and_list_wait_pile_roundtrip(store):
    obs = _observation(upload_id="u1", wait_pile=["some.deferred.metric"])
    store.save_metric_observation(obs)
    wait_pile = store.list_wait_pile_for_upload("u1")
    assert wait_pile == ["some.deferred.metric"]


def test_list_wait_pile_for_unknown_upload_returns_empty(store):
    assert store.list_wait_pile_for_upload("does-not-exist") == []


def test_save_metric_observation_generates_id_when_absent(store):
    obs = _observation(upload_id="u2")
    obs.pop("observation_id")
    store.save_metric_observation(obs)  # must not raise despite missing id
    assert store.list_wait_pile_for_upload("u2") == []


def test_save_metric_observation_empty_metrics_and_context(store):
    obs = {"producer": "p", "context": {}, "metrics": {}}
    store.save_metric_observation(obs)  # must not raise on minimal payload


def test_save_metric_observation_redacts_private_context(store):
    obs = _observation()
    obs["context"].update(
        {
            "staged_path": "/private/work/frame.fits",
            "observer_display_name": "test observer",
            "nested": {"email": "private address", "keep": "machine"},
        }
    )
    obs["metrics"]["ops.staging_path"] = "/private/work/frame.fits"
    store.save_metric_observation(obs)

    from db import session_scope

    with session_scope() as session:
        row = session.query(store.MetricObservation).one()
        context = json.loads(row.context_json)
        metrics = json.loads(row.metrics_json)
    assert "staged_path" not in context
    assert "observer_display_name" not in context
    assert context["nested"] == {"keep": "machine"}
    assert metrics == {"frame.zp": 22.0}


def test_list_l1_frames_for_night_empty_when_nothing_saved(store):
    frames = store.list_l1_frames_for_night("T1", "T1_2026-01-01")
    assert frames == []


def test_list_l1_frames_for_night_fallback_matches_by_night_id(store):
    obs = _observation(
        upload_id="u3",
        telescope_id="T1",
        metrics={"frame.zp": 21.5, "frame.fwhm_arcsec": 2.2, "frame.airmass": 1.1},
    )
    obs["context"]["night_id"] = "T1_2026-01-01"
    store.save_metric_observation(obs)

    frames = store.list_l1_frames_for_night("T1", "T1_2026-01-01")
    assert len(frames) == 1
    assert frames[0]["telescope_id"] == "T1"
    assert frames[0]["zp"] == 21.5
    assert frames[0]["fwhm_arcsec"] == 2.2
    assert frames[0]["airmass"] == 1.1


def test_list_l1_frames_for_night_fallback_excludes_other_nights(store):
    obs = _observation(upload_id="u4", telescope_id="T1")
    obs["context"]["night_id"] = "T1_2026-02-02"
    store.save_metric_observation(obs)

    frames = store.list_l1_frames_for_night("T1", "T1_2026-01-01")
    assert frames == []


def test_list_l1_frames_for_night_fallback_excludes_other_telescopes(store):
    obs = _observation(upload_id="u5", telescope_id="T2")
    obs["context"]["night_id"] = "T1_2026-01-01"
    store.save_metric_observation(obs)

    frames = store.list_l1_frames_for_night("T1", "T1_2026-01-01")
    assert frames == []


def test_list_l1_frames_for_night_bare_night_id_without_telescope_prefix(store):
    obs = _observation(upload_id="u6", telescope_id="T1")
    obs["context"]["night_id"] = "2026-03-03"
    store.save_metric_observation(obs)

    # night_id has no "_" -> night_day == night_id itself.
    frames = store.list_l1_frames_for_night("T1", "2026-03-03")
    assert len(frames) == 1
