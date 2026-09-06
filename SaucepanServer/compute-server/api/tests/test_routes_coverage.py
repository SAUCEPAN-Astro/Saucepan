"""Route-level edge-case coverage for /health, /v1/grade, /v1/stack, /v1/photometry.

Complements test_request_limits.py (DoS guards) and test_task_context_filter.py
(allowlist + provenance). This file focuses on auth failures, malformed input,
and the success/error branches of each handler that were previously untested.
"""

from __future__ import annotations

import json
from pathlib import Path
from unittest.mock import MagicMock

import pytest
from grading.fits_limits import FitsSizeLimitError


@pytest.fixture()
def storage_root(tmp_path):
    return tmp_path


@pytest.fixture()
def app_insecure(storage_root, monkeypatch):
    """App with COMPUTE_ALLOW_INSECURE=1 (auth short-circuited)."""
    monkeypatch.setenv("STORAGE_ROOT", str(storage_root))
    monkeypatch.setenv("COMPUTE_ALLOW_INSECURE", "1")
    monkeypatch.delenv("COMPUTE_TOKEN", raising=False)
    monkeypatch.delenv("COMPUTE_POST_INGEST", raising=False)
    monkeypatch.delenv("GRADES_INGEST_URL", raising=False)
    monkeypatch.delenv("FLASK_GRADES_URL", raising=False)

    from api.app import create_app

    app = create_app()
    app.config["TESTING"] = True
    return app


@pytest.fixture()
def client(app_insecure):
    return app_insecure.test_client()


@pytest.fixture()
def app_authed(storage_root, monkeypatch):
    """App with a real COMPUTE_TOKEN required."""
    monkeypatch.setenv("STORAGE_ROOT", str(storage_root))
    monkeypatch.setenv("COMPUTE_TOKEN", "s3cr3t")
    monkeypatch.delenv("COMPUTE_ALLOW_INSECURE", raising=False)

    from api.app import create_app

    app = create_app()
    app.config["TESTING"] = True
    return app


@pytest.fixture()
def authed_client(app_authed):
    return app_authed.test_client()


def _write_fits(storage_root: Path, name: str = "frame.fits") -> Path:
    fits = storage_root / name
    fits.write_bytes(b"SIMPLE  =                    T / stub\n")
    return fits


def _write_sidecar(fits: Path, **identity: str) -> None:
    Path(f"{fits}.sp_task.json").write_text(json.dumps(identity), encoding="utf-8")


# ── /health ───────────────────────────────────────────────────────────────


def test_health_route(client):
    resp = client.get("/health")
    assert resp.status_code == 200
    body = resp.get_json()
    assert body == {"status": "healthy", "service": "saucepan-compute"}


def test_normalize_route_writes_canonical_fits(client, storage_root):
    from astropy.io import fits

    raw = storage_root / "raw.fits"
    hdu = fits.PrimaryHDU(data=[[1.0, 2.0], [3.0, 4.0]])
    hdu.header["RA"] = 12.5
    hdu.header["DEC"] = -2.0
    hdu.header["TELESCOP"] = "demo-pier"
    hdu.header["FILTER"] = "R"
    hdu.header["EXPTIME"] = 30.0
    hdu.header["DATE-OBS"] = "2026-09-05T00:00:00Z"
    hdu.writeto(raw)

    response = client.post("/v1/normalize", json={"staged_path": "raw.fits"})

    assert response.status_code == 200
    body = response.get_json()
    normalized = Path(body["output_path"])
    assert normalized.is_file()
    assert body["normalization"]["tier"] == 1
    with fits.open(normalized) as hdul:
        assert hdul[0].header["SP_TELE"] == "demo-pier"
        assert hdul[1].name == "ORIGHDRS"


# ── create_app auth bootstrap ────────────────────────────────────────────


def test_create_app_exits_without_token_or_insecure(monkeypatch, tmp_path):
    monkeypatch.setenv("STORAGE_ROOT", str(tmp_path))
    monkeypatch.delenv("COMPUTE_TOKEN", raising=False)
    monkeypatch.delenv("COMPUTE_ALLOW_INSECURE", raising=False)

    from api.app import create_app

    with pytest.raises(SystemExit):
        create_app()


# ── auth failures (token configured, request missing/wrong bearer) ─────────


def test_grade_missing_auth_header_rejected(authed_client):
    resp = authed_client.post("/v1/grade", json={"staged_path": "frame.fits"})
    assert resp.status_code == 401
    assert resp.get_json()["error"] == "Unauthorized"


def test_grade_wrong_bearer_token_rejected(authed_client):
    resp = authed_client.post(
        "/v1/grade",
        json={"staged_path": "frame.fits"},
        headers={"Authorization": "Bearer wrong-token"},
    )
    assert resp.status_code == 401


def test_stack_missing_auth_rejected(authed_client):
    resp = authed_client.post(
        "/v1/stack",
        json={"frame_paths": ["a.fits", "b.fits"], "output_path": "out.fits"},
    )
    assert resp.status_code == 401


def test_photometry_missing_auth_rejected(authed_client):
    resp = authed_client.post("/v1/photometry", json={"staged_path": "frame.fits"})
    assert resp.status_code == 401


def test_grade_no_token_and_no_insecure_flag_returns_401(client, monkeypatch):
    """
    _require_auth() re-reads env at request time; simulate the case where the
    app was created insecure but the env changed before the request lands
    (e.g. a config reload) — this exercises the branch create_app() itself
    guards against at startup.
    """
    monkeypatch.delenv("COMPUTE_ALLOW_INSECURE", raising=False)
    monkeypatch.delenv("COMPUTE_TOKEN", raising=False)
    resp = client.post("/v1/grade", json={"staged_path": "frame.fits"})
    assert resp.status_code == 401
    assert "COMPUTE_TOKEN required" in resp.get_json()["error"]


def test_grade_correct_bearer_token_passes_auth(authed_client, storage_root, monkeypatch):
    _write_fits(storage_root)
    monkeypatch.setattr(
        "worker.grading.grade_frame",
        lambda *a, **k: {"upload_id": None, "headline": 1},
    )
    resp = authed_client.post(
        "/v1/grade",
        json={"staged_path": "frame.fits"},
        headers={"Authorization": "Bearer s3cr3t"},
    )
    assert resp.status_code == 200


# ── /v1/grade: malformed input ──────────────────────────────────────────


def test_grade_missing_staged_path(client):
    resp = client.post("/v1/grade", json={})
    assert resp.status_code == 400
    assert "staged_path required" in resp.get_json()["error"]


def test_grade_task_context_not_a_dict(client, storage_root):
    _write_fits(storage_root)
    resp = client.post(
        "/v1/grade",
        json={"staged_path": "frame.fits", "task_context": ["not", "a", "dict"]},
    )
    assert resp.status_code == 400
    assert "task_context must be an object" in resp.get_json()["error"]


def test_grade_staged_path_traversal_rejected(client, storage_root):
    resp = client.post(
        "/v1/grade",
        json={"staged_path": "../outside.fits"},
    )
    assert resp.status_code == 400
    assert "outside STORAGE_ROOT" in resp.get_json()["error"]


def test_grade_staged_path_not_found(client, storage_root):
    resp = client.post("/v1/grade", json={"staged_path": "missing.fits"})
    assert resp.status_code == 400
    assert "FITS not found" in resp.get_json()["error"]


def test_grade_malformed_json_body_treated_as_missing_staged_path(client):
    resp = client.post(
        "/v1/grade",
        data="{not valid json",
        content_type="application/json",
    )
    assert resp.status_code == 400
    assert "staged_path required" in resp.get_json()["error"]


def test_grade_wrong_content_type_ignored_as_no_body(client, storage_root):
    _write_fits(storage_root)
    resp = client.post(
        "/v1/grade",
        data='{"staged_path": "frame.fits"}',
        content_type="text/plain",
    )
    # get_json(silent=True) returns None for a mismatched content-type, so the
    # body is treated as empty and staged_path is reported missing.
    assert resp.status_code == 400
    assert "staged_path required" in resp.get_json()["error"]


# ── /v1/grade: worker.grading.grade_frame failure branches ─────────────


def test_grade_invalid_sidecar_json_returns_400(client, storage_root):
    """
    json.JSONDecodeError is a ValueError subclass, so a malformed .sp_task.json
    is caught by the `except ValueError` clause in routes.py and the response
    carries the parser's own message (no "invalid staged task sidecar:" prefix,
    which is reserved for genuine OSError). #487 removed the unreachable
    JSONDecodeError arm from the second `except`, which now catches OSError only.
    """
    fits = _write_fits(storage_root)
    sidecar = storage_root / f"{fits.name}.sp_task.json"
    sidecar.write_text("{not valid json", encoding="utf-8")

    resp = client.post("/v1/grade", json={"staged_path": "frame.fits"})
    assert resp.status_code == 400
    assert "Expecting property name" in resp.get_json()["error"]


def test_grade_fits_size_limit_error_returns_413(client, storage_root, monkeypatch):
    _write_fits(storage_root)

    def _raise(*a, **k):
        raise FitsSizeLimitError("frame is too large")

    monkeypatch.setattr("worker.grading.grade_frame", _raise)
    resp = client.post("/v1/grade", json={"staged_path": "frame.fits"})
    assert resp.status_code == 413
    assert "too large" in resp.get_json()["error"]


def test_grade_generic_exception_returns_500(client, storage_root, monkeypatch):
    _write_fits(storage_root)

    def _raise(*a, **k):
        raise RuntimeError("boom")

    monkeypatch.setattr("worker.grading.grade_frame", _raise)
    resp = client.post("/v1/grade", json={"staged_path": "frame.fits"})
    assert resp.status_code == 500
    assert "boom" in resp.get_json()["error"]


def test_grade_ingest_failed_status_when_callback_returns_falsy(client, storage_root, monkeypatch):
    fits = _write_fits(storage_root)
    _write_sidecar(fits, upload_id="u1")
    monkeypatch.setenv("COMPUTE_POST_INGEST", "1")
    monkeypatch.setenv("GRADES_INGEST_URL", "http://example.test/ingest")
    monkeypatch.setattr(
        "worker.grading.grade_frame",
        lambda *a, **k: {"upload_id": "u1", "headline": 1},
    )
    monkeypatch.setattr("api.routes.post_grade_to_ingest", lambda grade: None)

    resp = client.post(
        "/v1/grade",
        json={"staged_path": "frame.fits", "task_context": {"upload_id": "u1"}},
    )
    assert resp.status_code == 200
    assert resp.get_json()["ingest_status"] == "failed"


def test_grade_ingest_failed_status_when_callback_raises(client, storage_root, monkeypatch):
    fits = _write_fits(storage_root)
    _write_sidecar(fits, upload_id="u1")
    monkeypatch.setenv("COMPUTE_POST_INGEST", "1")
    monkeypatch.setenv("GRADES_INGEST_URL", "http://example.test/ingest")
    monkeypatch.setattr(
        "worker.grading.grade_frame",
        lambda *a, **k: {"upload_id": "u1", "headline": 1},
    )

    def _raise(grade):
        raise RuntimeError("network down")

    monkeypatch.setattr("api.routes.post_grade_to_ingest", _raise)

    resp = client.post(
        "/v1/grade",
        json={"staged_path": "frame.fits", "task_context": {"upload_id": "u1"}},
    )
    assert resp.status_code == 200
    assert resp.get_json()["ingest_status"] == "failed"


# ── /v1/stack ────────────────────────────────────────────────────────────


def test_stack_requires_two_frames(client):
    resp = client.post(
        "/v1/stack",
        json={"frame_paths": ["only-one.fits"], "output_path": "out.fits"},
    )
    assert resp.status_code == 400
    assert "at least 2" in resp.get_json()["error"]


def test_stack_requires_output_path(client, storage_root):
    _write_fits(storage_root, "a.fits")
    _write_fits(storage_root, "b.fits")
    resp = client.post(
        "/v1/stack",
        json={"frame_paths": ["a.fits", "b.fits"]},
    )
    assert resp.status_code == 400
    assert "output_path required" in resp.get_json()["error"]


def test_stack_input_frame_not_found(client, storage_root):
    _write_fits(storage_root, "a.fits")
    resp = client.post(
        "/v1/stack",
        json={"frame_paths": ["a.fits", "missing.fits"], "output_path": "out.fits"},
    )
    assert resp.status_code == 400
    assert "FITS not found" in resp.get_json()["error"]


def test_stack_cohort_check_read_headers_exception_returns_500(client, storage_root, monkeypatch):
    _write_fits(storage_root, "a.fits")
    _write_fits(storage_root, "b.fits")

    def _raise(path):
        raise OSError("corrupt fits")

    monkeypatch.setattr("grading.fits_reader.read_sp_headers", _raise)
    resp = client.post(
        "/v1/stack",
        json={
            "frame_paths": ["a.fits", "b.fits"],
            "output_path": "out.fits",
        },
    )
    assert resp.status_code == 500
    assert "corrupt fits" in resp.get_json()["error"]


def test_stack_cohort_mixed_emulator_science_rejected(client, storage_root, monkeypatch):
    _write_fits(storage_root, "a.fits")
    _write_fits(storage_root, "b.fits")

    headers_by_path = {
        str(storage_root / "a.fits"): {"sp_emulator": True},
        str(storage_root / "b.fits"): {"sp_emulator": False},
    }
    monkeypatch.setattr(
        "grading.fits_reader.read_sp_headers",
        lambda path: headers_by_path[path],
    )
    resp = client.post(
        "/v1/stack",
        json={"frame_paths": ["a.fits", "b.fits"], "output_path": "out.fits"},
    )
    assert resp.status_code == 400
    assert "Cannot mix emulator" in resp.get_json()["error"]


def test_stack_fits_size_limit_error_returns_413(client, storage_root, monkeypatch):
    _write_fits(storage_root, "a.fits")
    _write_fits(storage_root, "b.fits")
    monkeypatch.setattr("grading.fits_reader.read_sp_headers", lambda path: {"sp_emulator": False})

    def _raise(*a, **k):
        raise FitsSizeLimitError("stack too big")

    monkeypatch.setattr("saucepan_pipeline.stacking.stack_fits_files", _raise)
    resp = client.post(
        "/v1/stack",
        json={"frame_paths": ["a.fits", "b.fits"], "output_path": "out.fits"},
    )
    assert resp.status_code == 413
    assert "too big" in resp.get_json()["error"]


def test_stack_generic_exception_returns_500(client, storage_root, monkeypatch):
    _write_fits(storage_root, "a.fits")
    _write_fits(storage_root, "b.fits")
    monkeypatch.setattr("grading.fits_reader.read_sp_headers", lambda path: {"sp_emulator": False})

    def _raise(*a, **k):
        raise RuntimeError("stacking exploded")

    monkeypatch.setattr("saucepan_pipeline.stacking.stack_fits_files", _raise)
    resp = client.post(
        "/v1/stack",
        json={"frame_paths": ["a.fits", "b.fits"], "output_path": "out.fits"},
    )
    assert resp.status_code == 500
    assert "stacking exploded" in resp.get_json()["error"]


def test_stack_success_returns_summary_and_output_path(client, storage_root, monkeypatch):
    _write_fits(storage_root, "a.fits")
    _write_fits(storage_root, "b.fits")
    monkeypatch.setattr("grading.fits_reader.read_sp_headers", lambda path: {"sp_emulator": False})
    fake_summary = {"n_frames": 2, "status": "ok"}
    stack_mock = MagicMock(return_value=fake_summary)
    monkeypatch.setattr("saucepan_pipeline.stacking.stack_fits_files", stack_mock)

    resp = client.post(
        "/v1/stack",
        json={
            "frame_paths": ["a.fits", "b.fits"],
            "output_path": "out/stacked.fits",
            "max_psf_fwhm": 2.5,
        },
    )
    assert resp.status_code == 200
    body = resp.get_json()
    assert body["summary"] == fake_summary
    assert body["output_path"].endswith("out/stacked.fits")
    stack_mock.assert_called_once()
    _, kwargs = stack_mock.call_args
    assert kwargs["max_psf_fwhm"] == 2.5


# ── /v1/photometry ───────────────────────────────────────────────────────


def test_photometry_missing_staged_path(client):
    resp = client.post("/v1/photometry", json={})
    assert resp.status_code == 400
    assert "staged_path required" in resp.get_json()["error"]


def test_photometry_staged_path_not_found(client, storage_root):
    resp = client.post("/v1/photometry", json={"staged_path": "missing.fits"})
    assert resp.status_code == 400
    assert "FITS not found" in resp.get_json()["error"]


def test_photometry_success_returns_summary(client, storage_root, monkeypatch):
    _write_fits(storage_root)
    fake_summary = {"status": "ok", "zp": 25.0}
    monkeypatch.setattr("photometry.run_photometry", lambda *a, **k: dict(fake_summary))

    resp = client.post("/v1/photometry", json={"staged_path": "frame.fits"})
    assert resp.status_code == 200
    body = resp.get_json()
    assert body["summary"]["zp"] == 25.0
    assert body["staged_path"].endswith("frame.fits")


def test_photometry_run_lp_included_when_requested(client, storage_root, monkeypatch):
    _write_fits(storage_root)
    monkeypatch.setattr("photometry.run_photometry", lambda *a, **k: {"status": "ok"})
    monkeypatch.setattr("photometry.run_lp", lambda ctx, summary, fits_path=None: {"status": "ok"})

    resp = client.post(
        "/v1/photometry",
        json={"staged_path": "frame.fits", "run_lp": True},
    )
    assert resp.status_code == 200
    assert resp.get_json()["summary"]["lp"] == {"status": "ok"}


def test_photometry_generic_exception_returns_500(client, storage_root, monkeypatch):
    _write_fits(storage_root)

    def _raise(*a, **k):
        raise RuntimeError("photometry crashed")

    monkeypatch.setattr("photometry.run_photometry", _raise)
    resp = client.post("/v1/photometry", json={"staged_path": "frame.fits"})
    assert resp.status_code == 500
    assert "photometry crashed" in resp.get_json()["error"]


def test_photometry_defer_accepted_status_202(client, storage_root, monkeypatch):
    _write_fits(storage_root)
    pool_mock = MagicMock()
    pool_mock.try_submit.return_value = True
    monkeypatch.setattr("api.limits.deferred_photometry_pool", lambda: pool_mock)

    resp = client.post(
        "/v1/photometry",
        json={"staged_path": "frame.fits", "defer": True},
    )
    assert resp.status_code == 202
    body = resp.get_json()
    assert body["status"] == "accepted"
    assert body["defer"] is True
    pool_mock.try_submit.assert_called_once()
