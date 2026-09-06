"""Tests for POST /api/v1/uploads/presign and /complete (catalog + object store flow)."""

from __future__ import annotations

from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from catalog import Frame, Upload
from db import get_session_factory, init_db, session_scope


def _write_object_stub(bucket, key, dest):
    path = Path(dest)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(b"FITS stub")


def _mock_backend(**overrides) -> MagicMock:
    backend = MagicMock()
    backend.bucket_for_tier.return_value = "saucepan"
    backend.presign_upload.return_value = "https://minio.test/put-url"
    backend.head_object.return_value = {
        "size": 128,
        "etag": "abc123",
        "content_type": "application/fits",
    }
    backend.download_object.side_effect = _write_object_stub
    for key, value in overrides.items():
        setattr(backend, key, value)
    return backend


@pytest.fixture()
def catalog_db(tmp_path, monkeypatch):
    db_path = tmp_path / "catalog.db"
    monkeypatch.setenv("DATABASE_URL", f"sqlite:///{db_path}")
    import db as db_mod

    db_mod._engine = None
    db_mod._SessionLocal = None
    init_db()
    yield db_path
    db_mod._engine = None
    db_mod._SessionLocal = None


@pytest.fixture()
def client(tmp_path, monkeypatch, catalog_db, grading_token_env):
    monkeypatch.setenv("STORAGE_ROOT", str(tmp_path))
    monkeypatch.setenv("PIPELINE_MODE", "sync")
    monkeypatch.setenv("DISTRIBUTION_WORKER", "0")
    monkeypatch.setenv("MINIO_BUCKET", "saucepan")

    import routes.upload.staging as staging_mod

    staging_mod.storage_client.storage_root = str(tmp_path)

    from app import create_app

    app = create_app()
    app.config["TESTING"] = True
    app.config["DATABASE_URL"] = f"sqlite:///{catalog_db}"
    return app.test_client()


@patch("routes.upload.presign.get_storage_backend")
def test_presign_creates_upload_row(mock_get_backend, client, auth_headers):
    backend = _mock_backend()
    mock_get_backend.return_value = backend

    resp = client.post(
        "/api/v1/uploads/presign",
        json={
            "filename": "frame.fits",
            "campaign_id": "demo",
            "task_id": 42,
            "telescope_id": "node_001",
        },
        headers=auth_headers,
    )
    assert resp.status_code == 201
    data = resp.get_json()
    assert data["success"] is True
    assert data["presigned_url"] == "https://minio.test/put-url"
    assert data["bucket"] == "saucepan"
    assert "upload_id" in data
    assert data["object_key"].startswith("demo/42/")

    factory = get_session_factory()
    with factory() as session:
        upload = session.get(Upload, data["upload_id"])
        assert upload is not None
        assert upload.status == "pending"
        assert upload.filename == "frame.fits"

    backend.presign_upload.assert_called_once()


def test_presign_requires_fields(client, auth_headers):
    resp = client.post(
        "/api/v1/uploads/presign",
        json={"filename": "x.fits"},
        headers=auth_headers,
    )
    assert resp.status_code == 400


def test_presign_rejects_campaign_path_traversal(client, auth_headers):
    resp = client.post(
        "/api/v1/uploads/presign",
        json={"filename": "frame.fits", "campaign_id": "../../outside"},
        headers=auth_headers,
    )
    assert resp.status_code == 400
    assert "invalid campaign_id" in resp.get_json()["message"]


@patch("routes.upload.staging.on_upload_complete", return_value="grade_assessed")
@patch("routes.upload.staging.get_storage_backend")
@patch("routes.upload.presign.get_storage_backend")
def test_complete_verifies_object_and_creates_frame(
    mock_get_backend_presign,
    mock_get_backend_staging,
    mock_grade,
    client,
    tmp_path,
    auth_headers,
):
    backend = _mock_backend()
    mock_get_backend_presign.return_value = backend
    mock_get_backend_staging.return_value = backend

    presign = client.post(
        "/api/v1/uploads/presign",
        json={"filename": "frame.fits", "campaign_id": "demo"},
        headers=auth_headers,
    ).get_json()
    upload_id = presign["upload_id"]
    object_key = presign["object_key"]

    resp = client.post(
        "/api/v1/uploads/complete",
        json={
            "upload_id": upload_id,
            "observer": "private@example.invalid",
            "user_id": "private-user-id",
            "task_snapshot": {
                "max_psf_fwhm_arcsec": 4.0,
                "nested": {
                    "observer": "nested-private@example.invalid",
                    "email": "nested-private@example.invalid",
                    "userId": "nested-user",
                    "researcher_id": "nested-researcher",
                    "owner_id": "nested-owner",
                    "keep": "machine-value",
                },
            },
        },
        headers=auth_headers,
    )
    assert resp.status_code == 202
    data = resp.get_json()
    assert data["success"] is True
    assert data["upload_id"] == upload_id
    assert data["pipeline_status"] == "grade_assessed"
    assert data["file_path"] is None

    backend.head_object.assert_called_once_with("saucepan", object_key)
    backend.download_object.assert_called_once()
    mock_grade.assert_called_once_with(upload_id)

    factory = get_session_factory()
    with factory() as session:
        upload = session.get(Upload, upload_id)
        assert upload.status == "completed"
        assert "observer" not in (upload.metadata_json or {})
        assert "user_id" not in (upload.metadata_json or {})
        assert upload.metadata_json["task_snapshot"] == {
            "max_psf_fwhm_arcsec": 4.0,
            "nested": {"keep": "machine-value"},
        }
        frames = session.query(Frame).filter(Frame.upload_id == upload_id).all()
        assert len(frames) == 1
        assert frames[0].staged_path is None


@patch(
    "routes.upload.staging.on_upload_complete",
    side_effect=[RuntimeError("grading down"), "grade_assessed"],
)
@patch("routes.upload.staging.get_storage_backend")
@patch("routes.upload.presign.get_storage_backend")
def test_complete_hook_failure_rolls_back_for_retry(
    mock_get_backend_presign,
    mock_get_backend_staging,
    mock_grade,
    client,
    auth_headers,
):
    backend = _mock_backend()
    mock_get_backend_presign.return_value = backend
    mock_get_backend_staging.return_value = backend

    presign = client.post(
        "/api/v1/uploads/presign",
        json={"filename": "frame.fits", "campaign_id": "demo"},
        headers=auth_headers,
    ).get_json()
    upload_id = presign["upload_id"]

    failed = client.post(
        "/api/v1/uploads/complete",
        json={"upload_id": upload_id},
        headers=auth_headers,
    )
    assert failed.status_code == 502

    with session_scope() as session:
        upload = session.get(Upload, upload_id)
        assert upload.status == "pending"
        assert session.query(Frame).filter(Frame.upload_id == upload_id).count() == 0

    retried = client.post(
        "/api/v1/uploads/complete",
        json={"upload_id": upload_id},
        headers=auth_headers,
    )
    assert retried.status_code == 202
    assert retried.get_json()["pipeline_status"] == "grade_assessed"
    assert mock_grade.call_count == 2


@patch("routes.upload.staging.get_storage_backend")
@patch("routes.upload.presign.get_storage_backend")
def test_complete_missing_object(
    mock_get_backend_presign, mock_get_backend_staging, client, auth_headers
):
    backend = _mock_backend()
    backend.head_object.side_effect = FileNotFoundError("not found")
    mock_get_backend_presign.return_value = backend
    mock_get_backend_staging.return_value = backend

    presign = client.post(
        "/api/v1/uploads/presign",
        json={"filename": "missing.fits", "campaign_id": "demo"},
        headers=auth_headers,
    ).get_json()

    resp = client.post(
        "/api/v1/uploads/complete",
        json={"upload_id": presign["upload_id"]},
        headers=auth_headers,
    )
    assert resp.status_code == 404
    assert "Object not found" in resp.get_json()["message"]


@patch("routes.upload.staging.get_storage_backend")
def test_complete_catalog_failure_cleans_downloaded_staged_file(
    mock_get_backend_staging, client, tmp_path, auth_headers
):
    backend = _mock_backend()
    mock_get_backend_staging.return_value = backend

    with session_scope() as session:
        session.add(
            Upload(
                id="u-db-fail",
                status="pending",
                bucket="saucepan",
                object_key="demo/a.fits",
                filename="a.fits",
                campaign_id="demo",
            )
        )
        session.add(
            Frame(
                id="existing-frame",
                upload_id="u-db-fail",
                campaign_id="demo",
                object_key="demo/a.fits",
            )
        )

    resp = client.post(
        "/api/v1/uploads/complete",
        json={"upload_id": "u-db-fail"},
        headers=auth_headers,
    )

    assert resp.status_code == 502, resp.get_json()
    assert not (tmp_path / "staging" / "demo" / "u-db-fail" / "a.fits").exists()


def test_complete_unknown_upload_id(client, auth_headers):
    resp = client.post(
        "/api/v1/uploads/complete",
        json={"upload_id": "00000000-0000-0000-0000-000000000000"},
        headers=auth_headers,
    )
    assert resp.status_code == 404
