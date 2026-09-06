"""Additional edge-case tests for routes/upload/presign.py (presign + complete)."""

from __future__ import annotations

from unittest.mock import MagicMock, patch

import pytest

from db import init_db


@pytest.fixture()
def catalog_db(tmp_path, monkeypatch):
    db_path = tmp_path / "catalog.db"
    monkeypatch.setenv("DATABASE_URL", f"sqlite:///{db_path}")
    import db as db_mod

    db_mod._engine = None
    db_mod._SessionLocal = None
    init_db()
    yield
    db_mod._engine = None
    db_mod._SessionLocal = None


@pytest.fixture()
def client(tmp_path, monkeypatch, catalog_db, grading_token_env):
    monkeypatch.setenv("STORAGE_ROOT", str(tmp_path))
    monkeypatch.setenv("PIPELINE_MODE", "sync")
    monkeypatch.setenv("DISTRIBUTION_WORKER", "0")

    import routes.upload.staging as staging_mod

    staging_mod.storage_client.storage_root = str(tmp_path)

    from app import create_app

    app = create_app()
    app.config["TESTING"] = True
    return app.test_client()


@patch("routes.upload.presign.get_storage_backend")
def test_presign_backend_failure_returns_503(mock_get_backend, client, auth_headers):
    backend = MagicMock()
    backend.bucket_for_tier.return_value = "saucepan"
    backend.presign_upload.side_effect = RuntimeError("r2 unreachable")
    mock_get_backend.return_value = backend

    resp = client.post(
        "/api/v1/uploads/presign",
        json={"filename": "a.fits", "campaign_id": "camp1"},
        headers=auth_headers,
    )
    assert resp.status_code == 503
    assert "Presign failed" in resp.get_json()["message"]


@patch("routes.upload.presign.get_storage_backend")
def test_complete_already_completed_upload_returns_200(mock_get_backend, client, auth_headers):
    from catalog import Upload
    from db import session_scope

    with session_scope() as session:
        session.add(
            Upload(
                id="u-done",
                status="completed",
                bucket="b",
                object_key="camp1/a.fits",
                filename="a.fits",
                campaign_id="camp1",
            )
        )

    resp = client.post(
        "/api/v1/uploads/complete",
        json={"upload_id": "u-done"},
        headers=auth_headers,
    )
    assert resp.status_code == 200
    data = resp.get_json()
    assert data["success"] is True
    assert data["message"] == "Upload already completed"
    assert data["object_key"] == "camp1/a.fits"
    mock_get_backend.assert_not_called()


@patch("routes.upload.staging.on_upload_complete", return_value="grade_assessed")
@patch("routes.upload.staging.get_storage_backend")
def test_complete_generic_exception_returns_502(
    mock_get_backend_staging, mock_grade, client, auth_headers
):
    from catalog import Upload
    from db import session_scope

    # OSError (not RuntimeError) so it falls through to the generic 502
    # branch in complete_catalog_upload rather than the RuntimeError->404
    # branch (which is reserved for "not found"-style failures).
    backend = MagicMock()
    backend.head_object.return_value = {"size": 10, "etag": "x"}
    backend.download_object.side_effect = OSError("disk full")
    mock_get_backend_staging.return_value = backend

    with session_scope() as session:
        session.add(
            Upload(
                id="u-fail",
                status="pending",
                bucket="b",
                object_key="camp1/a.fits",
                filename="a.fits",
                campaign_id="camp1",
            )
        )

    resp = client.post(
        "/api/v1/uploads/complete",
        json={"upload_id": "u-fail"},
        headers=auth_headers,
    )
    assert resp.status_code == 502
    assert "Complete failed" in resp.get_json()["message"]
