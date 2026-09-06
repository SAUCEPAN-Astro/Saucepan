"""Task lifecycle tests for worker.grading: grade_frame() and the Lambda handler().

Mocks FITS I/O (read_sp_headers, quality.assess_fits) so these exercise the
real grading/orchestrate.py + grading/emulator_policy.py math end to end
without needing real pixel data on disk.
"""

from __future__ import annotations

import os
from pathlib import Path

import pytest
from worker import grading as worker_grading


@pytest.fixture(autouse=True)
def _staged_inputs(tmp_path, monkeypatch):
    monkeypatch.setenv("STORAGE_ROOT", str(tmp_path))
    for name in ("frame.fits", "direct/frame.fits", "staged/frame.fits"):
        path = tmp_path / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(b"stub")


def _headers(**overrides):
    base = {
        "sp_exptime": 30.0,
        "sp_filter": "R",
        "sp_fwhm": 2.0,
        "sp_calstat": "BDF",
        "sp_snr": None,
        "sp_qual": None,
        "sp_ra": 10.0,
        "sp_dec": 20.0,
        "sp_dateobs": "2026-01-01T00:00:00",
        "sp_emulator": False,
    }
    base.update(overrides)
    return base


def _quality_metrics(**overrides):
    base = {
        "snr": 40.0,
        "noise_adu": 5.0,
        "shape": [100, 100],
        "saturated_pixels": 0,
        "star_pixels": 500,
    }
    base.update(overrides)
    return base


def test_grade_frame_happy_path(monkeypatch):
    monkeypatch.setattr(worker_grading, "read_sp_headers", lambda path: _headers())
    monkeypatch.setattr(
        worker_grading.quality,
        "assess_fits",
        lambda path, update_fits=False: _quality_metrics(),
    )

    result = worker_grading.grade_frame("/tmp/frame.fits", {"upload_id": "u1", "task_id": "t1"})

    assert result["upload_id"] == "u1"
    assert result["task_id"] == "t1"
    assert result["grader_version"].endswith("-lambda")
    assert 0 <= result["headline"] <= 100
    assert result["sp_emulator"] is False
    assert result["data_tier"] == "science"
    assert result["science_eligible"] is True
    assert "dimensions" in result


def test_grade_frame_emulator_header_marks_not_science_eligible(monkeypatch):
    monkeypatch.setattr(worker_grading, "read_sp_headers", lambda path: _headers(sp_emulator=True))
    monkeypatch.setattr(
        worker_grading.quality,
        "assess_fits",
        lambda path, update_fits=False: _quality_metrics(),
    )

    result = worker_grading.grade_frame("/tmp/frame.fits", {"allow_emulator": True})
    assert result["sp_emulator"] is True
    assert result["data_tier"] == "emulator"
    assert result["science_eligible"] is False


def test_grade_frame_passes_update_fits_through(monkeypatch):
    captured = {}

    monkeypatch.setattr(worker_grading, "read_sp_headers", lambda path: _headers())

    def fake_assess(path, update_fits=False):
        captured["update_fits"] = update_fits
        return _quality_metrics()

    monkeypatch.setattr(worker_grading.quality, "assess_fits", fake_assess)

    worker_grading.grade_frame("/tmp/frame.fits", {}, update_fits=True)
    assert captured["update_fits"] is True


def test_grade_frame_measures_fwhm_when_missing_and_requested(monkeypatch):
    headers_calls = {"n": 0}

    def fake_read_headers(path):
        headers_calls["n"] += 1
        if headers_calls["n"] == 1:
            return _headers(sp_fwhm=None)
        # second read (post in-process measurement) reports the new FWHM
        return _headers(sp_fwhm=1.8)

    monkeypatch.setattr(worker_grading, "read_sp_headers", fake_read_headers)
    monkeypatch.setattr(
        worker_grading.quality,
        "assess_fits",
        lambda path, update_fits=False: _quality_metrics(fwhm_arcsec=1.8),
    )

    result = worker_grading.grade_frame("/tmp/frame.fits", {}, measure_fwhm_if_missing=True)
    assert headers_calls["n"] == 2
    assert result["sp_fwhm"] == 1.8


def test_grade_frame_measure_fwhm_requested_but_fit_returns_none(monkeypatch):
    headers_calls = {"n": 0}

    def fake_read_headers(path):
        headers_calls["n"] += 1
        return _headers(sp_fwhm=None)

    monkeypatch.setattr(worker_grading, "read_sp_headers", fake_read_headers)
    monkeypatch.setattr(
        worker_grading.quality,
        "assess_fits",
        lambda path, update_fits=False: _quality_metrics(fwhm_arcsec=None),
    )

    result = worker_grading.grade_frame("/tmp/frame.fits", {}, measure_fwhm_if_missing=True)
    # No second read attempted when the fit found nothing usable.
    assert headers_calls["n"] == 1
    assert result["sp_fwhm"] is None


def test_grade_frame_measure_fwhm_zero_value_does_not_reread(monkeypatch):
    headers_calls = {"n": 0}

    def fake_read_headers(path):
        headers_calls["n"] += 1
        return _headers(sp_fwhm=None)

    monkeypatch.setattr(worker_grading, "read_sp_headers", fake_read_headers)
    monkeypatch.setattr(
        worker_grading.quality,
        "assess_fits",
        lambda path, update_fits=False: _quality_metrics(fwhm_arcsec=0.0),
    )

    worker_grading.grade_frame("/tmp/frame.fits", {}, measure_fwhm_if_missing=True)
    assert headers_calls["n"] == 1


# ── handler() Lambda-style entrypoint ───────────────────────────────────


def test_handler_missing_path_and_s3_key_returns_400():
    resp = worker_grading.handler({})
    assert resp["statusCode"] == 400
    assert "path" in resp["body"]["error"]


def test_handler_rejects_path_outside_storage_root():
    resp = worker_grading.handler({"path": "../outside.fits"})
    assert resp["statusCode"] == 400
    assert "outside STORAGE_ROOT" in resp["body"]["error"]


def test_handler_uses_s3_key_as_path_fallback(monkeypatch):
    captured = {}

    def fake_grade_frame(path, task_context, **kwargs):
        captured["path"] = path
        return {"headline": 99}

    monkeypatch.setattr(worker_grading, "grade_frame", fake_grade_frame)
    resp = worker_grading.handler({"s3_key": "staged/frame.fits"})
    assert resp["statusCode"] == 200
    assert captured["path"] == str(Path(os.environ["STORAGE_ROOT"]) / "staged/frame.fits")
    assert resp["body"] == {"headline": 99}


def test_handler_path_takes_precedence_over_s3_key(monkeypatch):
    captured = {}

    def fake_grade_frame(path, task_context, **kwargs):
        captured["path"] = path
        return {"headline": 1}

    monkeypatch.setattr(worker_grading, "grade_frame", fake_grade_frame)
    worker_grading.handler({"path": "direct/frame.fits", "s3_key": "ignored.fits"})
    assert captured["path"] == str(Path(os.environ["STORAGE_ROOT"]) / "direct/frame.fits")


def test_handler_extracts_task_context_from_event(monkeypatch):
    captured = {}

    def fake_grade_frame(path, task_context, **kwargs):
        captured["task_context"] = task_context
        return {"headline": 1}

    monkeypatch.setattr(worker_grading, "grade_frame", fake_grade_frame)
    worker_grading.handler(
        {
            "path": "frame.fits",
            "upload_id": "u1",
            "not_allowlisted": "dropped",
        }
    )
    assert captured["task_context"] == {"upload_id": "u1"}


def test_handler_passes_update_fits_and_measure_fwhm_flags(monkeypatch):
    captured = {}

    def fake_grade_frame(path, task_context, *, update_fits=False, measure_fwhm_if_missing=False):
        captured["update_fits"] = update_fits
        captured["measure_fwhm_if_missing"] = measure_fwhm_if_missing
        return {"headline": 1}

    monkeypatch.setattr(worker_grading, "grade_frame", fake_grade_frame)
    worker_grading.handler(
        {"path": "frame.fits", "update_fits": True, "measure_fwhm_if_missing": True}
    )
    assert captured["update_fits"] is True
    assert captured["measure_fwhm_if_missing"] is True


def test_handler_catches_exception_and_returns_500(monkeypatch):
    def _raise(*a, **k):
        raise ValueError("bad frame")

    monkeypatch.setattr(worker_grading, "grade_frame", _raise)
    resp = worker_grading.handler({"path": "frame.fits"})
    assert resp["statusCode"] == 500
    assert "bad frame" in resp["body"]["error"]
