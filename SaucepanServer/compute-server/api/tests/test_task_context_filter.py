"""HTTP grade path: task_context allowlist + post_ingest gate (issue #252)."""

from __future__ import annotations

import json
import sys
import types
from pathlib import Path
from unittest.mock import MagicMock

import pytest
from worker.context import (
    STAGED_TASK_SIDECAR_SUFFIX,
    apply_staged_provenance,
    extract_task_context,
)


@pytest.fixture()
def storage_root(tmp_path):
    return tmp_path


@pytest.fixture()
def client(storage_root, monkeypatch):
    monkeypatch.setenv("STORAGE_ROOT", str(storage_root))
    monkeypatch.setenv("COMPUTE_ALLOW_INSECURE", "1")
    monkeypatch.delenv("COMPUTE_POST_INGEST", raising=False)
    monkeypatch.delenv("GRADES_INGEST_URL", raising=False)
    monkeypatch.delenv("FLASK_GRADES_URL", raising=False)

    from api.app import create_app

    app = create_app()
    app.config["TESTING"] = True
    return app.test_client()


def _stub_worker_grading(monkeypatch, grade_frame):
    """Install a fake worker.grading so tests skip saucepan_pipeline."""
    grading_mod = types.ModuleType("worker.grading")
    grading_mod.grade_frame = grade_frame
    monkeypatch.setitem(sys.modules, "worker.grading", grading_mod)
    import worker

    monkeypatch.setattr(worker, "grading", grading_mod, raising=False)
    return grade_frame


def _write_fits(storage_root: Path, name: str = "frame.fits") -> Path:
    fits = storage_root / name
    fits.write_bytes(b"SIMPLE  =                    T / stub\n")
    return fits


def _write_sidecar(fits: Path, **identity: str) -> None:
    Path(f"{fits}{STAGED_TASK_SIDECAR_SUFFIX}").write_text(
        json.dumps(identity), encoding="utf-8"
    )


def test_extract_task_context_drops_non_allowlisted_keys():
    filtered = extract_task_context(
        {
            "upload_id": "u1",
            "task_id": "t1",
            "campaign_id": "forged-campaign",
            "frame_id": "forged-frame",
            "object_key": "evil/key",
            "s3_key": "evil/s3",
        }
    )
    assert filtered == {"upload_id": "u1", "task_id": "t1"}
    assert "campaign_id" not in filtered
    assert "frame_id" not in filtered


def test_apply_staged_provenance_rejects_forged_upload_id(tmp_path):
    fits = tmp_path / "frame.fits"
    fits.write_bytes(b"stub")
    sidecar = Path(f"{fits}{STAGED_TASK_SIDECAR_SUFFIX}")
    sidecar.write_text(
        json.dumps({"upload_id": "real-upload", "task_id": "real-task"}),
        encoding="utf-8",
    )

    with pytest.raises(ValueError, match="upload_id"):
        apply_staged_provenance(
            fits,
            {"upload_id": "forged-upload", "task_id": "real-task"},
        )


def test_apply_staged_provenance_rejects_forged_task_id(tmp_path):
    fits = tmp_path / "frame.fits"
    fits.write_bytes(b"stub")
    sidecar = Path(f"{fits}{STAGED_TASK_SIDECAR_SUFFIX}")
    sidecar.write_text(
        json.dumps({"upload_id": "real-upload", "task_id": "real-task"}),
        encoding="utf-8",
    )

    with pytest.raises(ValueError, match="task_id"):
        apply_staged_provenance(
            fits,
            {"upload_id": "real-upload", "task_id": "forged-task"},
        )


def test_grade_rejects_forged_context_when_sidecar_present(client, storage_root, monkeypatch):
    fits = _write_fits(storage_root)
    sidecar = Path(f"{fits}{STAGED_TASK_SIDECAR_SUFFIX}")
    sidecar.write_text(
        json.dumps({"upload_id": "real-upload", "task_id": "real-task"}),
        encoding="utf-8",
    )

    grade_mock = MagicMock(return_value={"upload_id": "real-upload", "headline": 1})
    _stub_worker_grading(monkeypatch, grade_mock)

    resp = client.post(
        "/v1/grade",
        json={
            "staged_path": "frame.fits",
            "task_context": {
                "upload_id": "forged-upload",
                "task_id": "real-task",
                "campaign_id": "should-be-stripped",
            },
            "post_ingest": True,
        },
    )
    assert resp.status_code == 400
    assert "upload_id" in resp.get_json()["error"]
    grade_mock.assert_not_called()


def test_grade_filters_task_context_keys(client, storage_root, monkeypatch):
    fits = _write_fits(storage_root)
    _write_sidecar(fits, upload_id="u1", task_id="t1")
    captured: dict = {}

    def fake_grade(fits_path, task_context, *, update_fits=False):
        captured["task_context"] = dict(task_context)
        return {"headline": 42, "upload_id": task_context.get("upload_id")}

    _stub_worker_grading(monkeypatch, fake_grade)

    resp = client.post(
        "/v1/grade",
        json={
            "staged_path": "frame.fits",
            "task_context": {
                "upload_id": "u1",
                "task_id": "t1",
                "telescope_id": "tele-1",
                "campaign_id": "forged",
                "frame_id": "forged-frame",
                "object_key": "evil",
            },
        },
    )
    assert resp.status_code == 200
    ctx = captured["task_context"]
    assert ctx["upload_id"] == "u1"
    assert ctx["task_id"] == "t1"
    assert ctx["telescope_id"] == "tele-1"
    assert "campaign_id" not in ctx
    assert "frame_id" not in ctx
    assert "object_key" not in ctx


def test_post_ingest_ignored_without_env(client, storage_root, monkeypatch):
    fits = _write_fits(storage_root)
    _write_sidecar(fits, upload_id="u1")
    _stub_worker_grading(
        monkeypatch,
        lambda *a, **k: {"upload_id": "u1", "headline": 1},
    )
    ingest = MagicMock(return_value="ok")
    monkeypatch.setattr("api.routes.post_grade_to_ingest", ingest)
    monkeypatch.setenv("GRADES_INGEST_URL", "http://example.test/ingest")

    resp = client.post(
        "/v1/grade",
        json={
            "staged_path": "frame.fits",
            "task_context": {"upload_id": "u1"},
            "post_ingest": True,
        },
    )
    assert resp.status_code == 200
    assert resp.get_json()["ingest_status"] is None
    ingest.assert_not_called()


def test_post_ingest_runs_when_env_enabled(client, storage_root, monkeypatch):
    fits = _write_fits(storage_root)
    _write_sidecar(fits, upload_id="u1")
    monkeypatch.setenv("COMPUTE_POST_INGEST", "1")
    monkeypatch.setenv("GRADES_INGEST_URL", "http://example.test/ingest")
    _stub_worker_grading(
        monkeypatch,
        lambda *a, **k: {"upload_id": "u1", "headline": 1},
    )
    ingest = MagicMock(return_value="ok")
    monkeypatch.setattr("api.routes.post_grade_to_ingest", ingest)

    resp = client.post(
        "/v1/grade",
        json={
            "staged_path": "frame.fits",
            "task_context": {"upload_id": "u1"},
            "post_ingest": False,
        },
    )
    assert resp.status_code == 200
    assert resp.get_json()["ingest_status"] == "success"
    ingest.assert_called_once()
