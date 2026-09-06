"""Additional edge-case tests for routes/upload/download.py (frame download presign)."""

from __future__ import annotations

from unittest.mock import MagicMock, patch

import pytest


@pytest.fixture()
def catalog_db(tmp_path, monkeypatch):
    db_path = tmp_path / "catalog.db"
    monkeypatch.setenv("DATABASE_URL", f"sqlite:///{db_path}")
    import db as db_mod

    db_mod._engine = None
    db_mod._SessionLocal = None
    db_mod.init_db()
    yield
    db_mod._engine = None
    db_mod._SessionLocal = None


@pytest.fixture()
def client(tmp_path, monkeypatch, catalog_db, grading_token_env):
    monkeypatch.setenv("STORAGE_ROOT", str(tmp_path))
    monkeypatch.setenv("DISTRIBUTION_WORKER", "0")

    from app import create_app

    app = create_app()
    app.config["TESTING"] = True
    return app.test_client()


def test_download_frame_not_found(client, auth_headers):
    resp = client.get("/api/v1/frames/nonexistent/download", headers=auth_headers)
    assert resp.status_code == 404
    assert "Frame not found" in resp.get_json()["message"]


def test_download_upload_not_found_for_orphaned_frame(client, auth_headers):
    """A Frame row whose parent Upload was deleted must 404, not 500."""
    from catalog import Frame
    from db import session_scope

    with session_scope() as session:
        session.add(
            Frame(
                id="orphan-frame",
                upload_id="missing-upload",
                campaign_id="camp1",
                object_key="camp1/orphan.fits",
            )
        )

    resp = client.get("/api/v1/frames/orphan-frame/download", headers=auth_headers)
    assert resp.status_code == 404
    assert "Upload not found" in resp.get_json()["message"]


@patch("routes.upload.download.get_storage_backend")
def test_download_presign_failure_returns_503(mock_get_backend, client, auth_headers):
    from catalog import Frame, Upload
    from db import session_scope

    backend = MagicMock()
    backend.supports_client_download = True
    backend.presign_download.side_effect = RuntimeError("r2 unreachable")
    mock_get_backend.return_value = backend

    with session_scope() as session:
        session.add(
            Upload(
                id="u1",
                status="completed",
                bucket="b",
                object_key="camp1/a.fits",
                filename="a.fits",
                campaign_id="camp1",
            )
        )
        session.add(
            Frame(
                id="f1",
                upload_id="u1",
                campaign_id="camp1",
                object_key="camp1/a.fits",
            )
        )

    resp = client.get("/api/v1/frames/f1/download", headers=auth_headers)
    assert resp.status_code == 503
    assert resp.get_json()["message"] == "Presign download failed"
    assert "r2 unreachable" not in resp.get_json()["message"]
