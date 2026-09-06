"""Unit tests for storage backend factory and frame download route."""

from __future__ import annotations

from unittest.mock import MagicMock, patch

import pytest

from storage.factory import get_storage_backend, reset_storage_backend
from storage.filesystem_backend import FilesystemBackend


@pytest.fixture(autouse=True)
def _clear_backend_cache():
    reset_storage_backend()
    yield
    reset_storage_backend()


def test_factory_defaults_to_filesystem(monkeypatch, tmp_path):
    monkeypatch.delenv("STORAGE_BACKEND", raising=False)
    monkeypatch.setenv("STORAGE_ROOT", str(tmp_path))
    backend = get_storage_backend()
    assert isinstance(backend, FilesystemBackend)
    assert backend.name == "filesystem"


def test_factory_selects_r2(monkeypatch):
    from storage.r2_backend import R2Backend

    monkeypatch.setenv("STORAGE_BACKEND", "r2")
    monkeypatch.setenv("R2_ACCOUNT_ID", "test-account")
    monkeypatch.setenv("R2_ACCESS_KEY_ID", "test-key")
    monkeypatch.setenv("R2_SECRET_ACCESS_KEY", "test-secret")
    reset_storage_backend()
    backend = get_storage_backend()
    assert isinstance(backend, R2Backend)


def test_factory_rejects_minio(monkeypatch):
    monkeypatch.setenv("STORAGE_BACKEND", "minio")
    reset_storage_backend()
    with pytest.raises(ValueError, match="minio was removed"):
        get_storage_backend()


def test_factory_rejects_unknown_backend(monkeypatch):
    monkeypatch.setenv("STORAGE_BACKEND", "s3")
    reset_storage_backend()
    with pytest.raises(ValueError, match="Unknown STORAGE_BACKEND"):
        get_storage_backend()


def test_frame_download_returns_presigned_url(tmp_path, monkeypatch):
    db_path = tmp_path / "catalog.db"
    monkeypatch.setenv("DATABASE_URL", f"sqlite:///{db_path}")
    monkeypatch.setenv("STORAGE_ROOT", str(tmp_path))
    monkeypatch.setenv("DISTRIBUTION_WORKER", "0")

    import db as db_mod

    db_mod._engine = None
    db_mod._SessionLocal = None
    db_mod.init_db()

    backend = MagicMock()
    backend.supports_client_download = True
    backend.presign_download.return_value = "https://r2.test/get-url"

    from catalog import Frame, Upload
    from db import session_scope

    upload_id = "upload-dl-1"
    frame_id = "frame-dl-1"
    with session_scope() as session:
        session.add(
            Upload(
                id=upload_id,
                status="completed",
                bucket="saucepan",
                object_key="demo/frame.fits",
                filename="frame.fits",
                campaign_id="demo",
            )
        )
        session.add(
            Frame(
                id=frame_id,
                upload_id=upload_id,
                campaign_id="demo",
                object_key="demo/frame.fits",
                staged_path=str(tmp_path / "staging/demo/frame.fits"),
            )
        )

    with patch("routes.upload.download.get_storage_backend", return_value=backend):
        from app import create_app

        app = create_app()
        app.config["TESTING"] = True
        client = app.test_client()

        resp = client.get(f"/api/v1/frames/{frame_id}/download")
    assert resp.status_code == 200
    data = resp.get_json()
    assert data["presigned_url"] == "https://r2.test/get-url"
    backend.presign_download.assert_called_once()
