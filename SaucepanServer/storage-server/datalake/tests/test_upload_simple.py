"""Tests for routes/upload/simple.py — single-shot multipart upload."""

from __future__ import annotations

import io
from pathlib import Path
from unittest.mock import patch

import pytest

from routes.upload.simple import _campaign_wants_stack


# ── _campaign_wants_stack: pure function ─────────────────────────────────


class _FakeForm(dict):
    def get(self, key, default=None):
        return super().get(key, default)


def test_campaign_wants_stack_via_product_json_mode():
    form = _FakeForm({"product_json": '{"mode": "stack"}'})
    assert _campaign_wants_stack(form) is True


def test_campaign_wants_stack_via_product_dict_mode():
    form = _FakeForm({"product": '{"mode": "STACK"}'})
    assert _campaign_wants_stack(form) is True


def test_campaign_wants_stack_via_product_mode_field():
    form = _FakeForm({"product_mode": "stack"})
    assert _campaign_wants_stack(form) is True


def test_campaign_wants_stack_default_false():
    form = _FakeForm({})
    assert _campaign_wants_stack(form) is False


def test_campaign_wants_stack_invalid_json_falls_back_to_product_mode():
    form = _FakeForm({"product": "not-json", "product_mode": "stack"})
    assert _campaign_wants_stack(form) is True


def test_campaign_wants_stack_photometry_mode_is_false():
    form = _FakeForm({"product_mode": "per_frame"})
    assert _campaign_wants_stack(form) is False


# ── simple_file_upload route ──────────────────────────────────────────────


@pytest.fixture()
def client(tmp_path, monkeypatch, grading_token_env):
    db_path = tmp_path / "catalog.db"
    monkeypatch.setenv("STORAGE_ROOT", str(tmp_path))
    monkeypatch.setenv("DATABASE_URL", f"sqlite:///{db_path}")
    monkeypatch.setenv("DISTRIBUTION_WORKER", "0")
    monkeypatch.delenv("COMPUTE_URL", raising=False)

    import db as db_mod

    db_mod._engine = None
    db_mod._SessionLocal = None

    import routes.upload.staging as staging_mod

    staging_mod.storage_client.storage_root = str(tmp_path)

    from app import create_app

    app = create_app()
    app.config["TESTING"] = True
    yield app.test_client()

    db_mod._engine = None
    db_mod._SessionLocal = None


def test_simple_upload_requires_file(client, auth_headers):
    resp = client.post(
        "/api/v1/uploads/simple-upload",
        data={"campaign_id": "camp1"},
        headers=auth_headers,
        content_type="multipart/form-data",
    )
    assert resp.status_code == 400
    assert "No file provided" in resp.get_json()["message"]


def test_simple_upload_requires_campaign_id(client, auth_headers):
    resp = client.post(
        "/api/v1/uploads/simple-upload",
        data={"file": (io.BytesIO(b"fits-data"), "frame.fits")},
        headers=auth_headers,
        content_type="multipart/form-data",
    )
    assert resp.status_code == 400
    assert "campaign_id is required" in resp.get_json()["message"]


def test_simple_upload_rejects_campaign_path_traversal(client, auth_headers):
    resp = client.post(
        "/api/v1/uploads/simple-upload",
        data={"file": (io.BytesIO(b"fits-data"), "frame.fits"), "campaign_id": "../../escape"},
        headers=auth_headers,
        content_type="multipart/form-data",
    )
    assert resp.status_code == 400
    assert "invalid campaign_id" in resp.get_json()["message"]


def test_simple_upload_success_without_compute_url(client, auth_headers):
    resp = client.post(
        "/api/v1/uploads/simple-upload",
        data={"file": (io.BytesIO(b"fits-data"), "frame.fits"), "campaign_id": "camp1"},
        headers=auth_headers,
        content_type="multipart/form-data",
    )
    assert resp.status_code == 202
    data = resp.get_json()
    assert data["success"] is True
    assert data["pipeline_status"] == "compute_unconfigured"
    assert data["file_path"] is None
    assert data["upload_id"]

    from catalog import Frame, Upload
    from db import session_scope

    with session_scope() as session:
        upload = session.get(Upload, data["upload_id"])
        frame = session.query(Frame).filter(Frame.upload_id == data["upload_id"]).one()
        assert upload is not None
        assert upload.campaign_id == "camp1"
        assert frame.staged_path is None


@patch("routes.upload.simple.storage_client.upload_to_staging")
def test_simple_upload_temp_file_is_private(mock_staging, client, auth_headers):
    observed_mode = {}

    def inspect_temp_file(path, _campaign_id):
        observed_mode["mode"] = Path(path).stat().st_mode & 0o777
        return {"success": False}

    mock_staging.side_effect = inspect_temp_file
    resp = client.post(
        "/api/v1/uploads/simple-upload",
        data={"file": (io.BytesIO(b"fits-data"), "frame.fits"), "campaign_id": "camp1"},
        headers=auth_headers,
        content_type="multipart/form-data",
    )
    assert resp.status_code == 500
    assert observed_mode["mode"] == 0o600


@patch("routes.upload.simple.request_photometry")
def test_simple_upload_with_compute_url_runs_photometry(
    mock_photometry, client, auth_headers, monkeypatch
):
    monkeypatch.setenv("COMPUTE_URL", "http://compute.test")
    mock_photometry.return_value = {"lp": None}

    resp = client.post(
        "/api/v1/uploads/simple-upload",
        data={"file": (io.BytesIO(b"fits-data"), "frame.fits"), "campaign_id": "camp1"},
        headers=auth_headers,
        content_type="multipart/form-data",
    )
    assert resp.status_code == 202
    data = resp.get_json()
    assert "photometry" in data["pipeline_status"]
    mock_photometry.assert_called_once()


@patch("routes.upload.simple.request_photometry", side_effect=RuntimeError("compute unreachable"))
def test_simple_upload_photometry_error_is_captured_in_status(
    mock_photometry, client, auth_headers, monkeypatch
):
    monkeypatch.setenv("COMPUTE_URL", "http://compute.test")
    resp = client.post(
        "/api/v1/uploads/simple-upload",
        data={"file": (io.BytesIO(b"fits-data"), "frame.fits"), "campaign_id": "camp1"},
        headers=auth_headers,
        content_type="multipart/form-data",
    )
    assert resp.status_code == 202
    data = resp.get_json()
    assert "photometry_error" in data["pipeline_status"]
    assert "compute unreachable" not in data["pipeline_status"]
