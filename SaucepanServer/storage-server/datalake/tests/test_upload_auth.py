"""Tests for DATALAKE_GRADING_TOKEN bearer auth on upload routes."""

from __future__ import annotations

from unittest.mock import MagicMock, patch

import pytest
from pathlib import Path


@pytest.fixture()
def client(tmp_path, monkeypatch, grading_token_env):
    db_path = tmp_path / "catalog.db"
    monkeypatch.setenv("STORAGE_ROOT", str(tmp_path))
    monkeypatch.setenv("DATABASE_URL", f"sqlite:///{db_path}")
    monkeypatch.setenv("PIPELINE_MODE", "sync")
    monkeypatch.setenv("COMPUTE_URL", "http://compute.test")
    monkeypatch.setenv("DISTRIBUTION_WORKER", "0")

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


def test_complete_401_without_token(client):
    resp = client.post(
        "/api/v1/uploads/complete",
        json={"upload_id": "upload-auth-1"},
    )
    assert resp.status_code == 401
    assert resp.get_json()["success"] is False


def test_complete_401_wrong_token(client):
    resp = client.post(
        "/api/v1/uploads/complete",
        json={"upload_id": "upload-auth-1"},
        headers={"Authorization": "Bearer wrong-token"},
    )
    assert resp.status_code == 401
    assert resp.get_json()["message"] == "Unauthorized"


@patch("routes.upload.staging.on_upload_complete", return_value="grade_assessed")
@patch("routes.upload.staging.get_storage_backend")
def test_complete_202_with_token(
    mock_get_backend,
    mock_grade_hook,
    client,
    auth_headers,
):
    def _write_object_stub(bucket, key, dest):
        path = Path(dest)
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(b"FITS stub")

    backend = MagicMock()
    backend.bucket_for_tier.return_value = "saucepan"
    backend.head_object.return_value = {"size": 128, "etag": "abc"}
    backend.download_object.side_effect = _write_object_stub
    mock_get_backend.return_value = backend

    from catalog import Upload
    from db import session_scope

    with session_scope() as session:
        session.add(
            Upload(
                id="upload-auth-1",
                status="pending",
                bucket="saucepan",
                object_key="demo/frame.fits",
                filename="frame.fits",
                campaign_id="demo",
            )
        )

    resp = client.post(
        "/api/v1/uploads/complete",
        json={"upload_id": "upload-auth-1"},
        headers=auth_headers,
    )
    assert resp.status_code == 202
    assert resp.get_json()["success"] is True


def test_presign_401_without_token(client):
    resp = client.post(
        "/api/v1/uploads/presign",
        json={"filename": "frame.fits", "campaign_id": "demo"},
    )
    assert resp.status_code == 401


@patch("routes.upload.presign.get_storage_backend")
def test_presign_201_with_token(mock_get_backend, client, auth_headers):
    backend = MagicMock()
    backend.bucket_for_tier.return_value = "saucepan"
    backend.presign_upload.return_value = "https://minio.test/put-url"
    mock_get_backend.return_value = backend

    resp = client.post(
        "/api/v1/uploads/presign",
        json={"filename": "frame.fits", "campaign_id": "demo"},
        headers=auth_headers,
    )
    assert resp.status_code == 201
    assert resp.get_json()["success"] is True


def test_startup_refuses_without_token(monkeypatch):
    monkeypatch.delenv("DATALAKE_GRADING_TOKEN", raising=False)
    monkeypatch.delenv("DATALAKE_ALLOW_INSECURE", raising=False)
    monkeypatch.delenv("FLASK_ENV", raising=False)

    import auth as auth_mod

    with pytest.raises(SystemExit) as exc:
        auth_mod.ensure_grading_token_at_startup()
    assert exc.value.code == 1


def test_startup_allows_insecure_mode(monkeypatch):
    monkeypatch.delenv("DATALAKE_GRADING_TOKEN", raising=False)
    monkeypatch.setenv("DATALAKE_ALLOW_INSECURE", "1")

    import auth as auth_mod

    auth_mod.ensure_grading_token_at_startup()
