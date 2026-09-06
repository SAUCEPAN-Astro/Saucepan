"""Tests for the legacy chunked upload session routes (routes/upload/sessions.py)
and the corresponding /uploads/complete chunk-completion fallback in presign.py.
"""

from __future__ import annotations

import base64
from pathlib import Path
from unittest.mock import patch

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

    import routes.upload.staging as staging_mod
    import routes.upload.sessions as sessions_mod

    staging_mod.storage_client.storage_root = str(tmp_path)
    # Isolate the in-memory session dict per test.
    sessions_mod.upload_sessions.clear()

    from app import create_app

    app = create_app()
    app.config["TESTING"] = True
    yield app.test_client()
    sessions_mod.upload_sessions.clear()


def test_create_upload_session_requires_campaign_id(client, auth_headers):
    resp = client.post("/api/v1/uploads", headers=auth_headers)
    assert resp.status_code == 400


def test_create_upload_session_success(client, auth_headers):
    resp = client.post(
        "/api/v1/uploads?campaign_id=camp1&dataset_name=ds1",
        headers=auth_headers,
    )
    assert resp.status_code == 202
    data = resp.get_json()
    assert data["success"] is True
    assert "upload_id" in data


def test_get_upload_session_not_found(client, auth_headers):
    resp = client.get("/api/v1/uploads/sessions/9999", headers=auth_headers)
    assert resp.status_code == 404


def test_get_upload_session_found(client, auth_headers):
    create = client.post("/api/v1/uploads?campaign_id=camp1", headers=auth_headers).get_json()
    upload_id = create["upload_id"]
    resp = client.get(f"/api/v1/uploads/sessions/{upload_id}", headers=auth_headers)
    assert resp.status_code == 200
    assert resp.get_json()["id"] == upload_id


def test_cancel_upload_session_not_found(client, auth_headers):
    resp = client.delete("/api/v1/uploads/sessions/9999", headers=auth_headers)
    assert resp.status_code == 404


def test_cancel_upload_session_success(client, auth_headers):
    create = client.post("/api/v1/uploads?campaign_id=camp1", headers=auth_headers).get_json()
    upload_id = create["upload_id"]
    resp = client.delete(f"/api/v1/uploads/sessions/{upload_id}", headers=auth_headers)
    assert resp.status_code == 200

    follow_up = client.get(f"/api/v1/uploads/sessions/{upload_id}", headers=auth_headers)
    assert follow_up.status_code == 404


def test_upload_chunk_requires_json_body(client, auth_headers):
    resp = client.post("/api/v1/uploads/chunks", headers=auth_headers)
    assert resp.status_code == 400


def test_upload_chunk_unknown_session(client, auth_headers):
    resp = client.post(
        "/api/v1/uploads/chunks",
        json={"upload_id": 9999, "chunk_index": 0, "chunk_data_base64": ""},
        headers=auth_headers,
    )
    assert resp.status_code == 404


def test_upload_chunk_rejects_non_integer_index(client, auth_headers):
    create = client.post("/api/v1/uploads?campaign_id=camp1", headers=auth_headers).get_json()
    resp = client.post(
        "/api/v1/uploads/chunks",
        json={"upload_id": create["upload_id"], "chunk_index": "../../escape", "chunk_data_base64": ""},
        headers=auth_headers,
    )
    assert resp.status_code == 400


def test_upload_chunk_success(client, auth_headers):
    create = client.post("/api/v1/uploads?campaign_id=camp1", headers=auth_headers).get_json()
    upload_id = create["upload_id"]

    chunk_b64 = base64.b64encode(b"hello-chunk").decode("ascii")
    resp = client.post(
        "/api/v1/uploads/chunks",
        json={"upload_id": upload_id, "chunk_index": 0, "chunk_data_base64": chunk_b64},
        headers=auth_headers,
    )
    assert resp.status_code == 200
    data = resp.get_json()
    assert data["success"] is True
    assert data["chunk_index"] == 0


@patch(
    "routes.upload.presign.run_post_upload_grading",
    return_value=("compute_unconfigured", None, None),
)
def test_full_chunked_upload_completion_flow(mock_grading, client, auth_headers):
    """Create session → upload one chunk → complete via /uploads/complete."""
    create = client.post("/api/v1/uploads?campaign_id=camp1", headers=auth_headers).get_json()
    upload_id = create["upload_id"]

    chunk_b64 = base64.b64encode(b"fits-file-bytes").decode("ascii")
    chunk_resp = client.post(
        "/api/v1/uploads/chunks",
        json={"upload_id": upload_id, "chunk_index": 0, "chunk_data_base64": chunk_b64},
        headers=auth_headers,
    )
    assert chunk_resp.status_code == 200

    complete_resp = client.post(
        "/api/v1/uploads/complete",
        json={"upload_id": upload_id, "total_chunks": 1, "task_id": "t1"},
        headers=auth_headers,
    )
    assert complete_resp.status_code == 200
    data = complete_resp.get_json()
    assert data["success"] is True
    assert data["checksum"]
    assert data["file_size"] == len(b"fits-file-bytes")
    assert data["file_path"] is None
    mock_grading.assert_called_once()


@patch(
    "routes.upload.presign.run_post_upload_grading",
    return_value=("compute_unconfigured", None, None),
)
@patch("routes.upload.presign.storage_client.upload_to_staging")
def test_failed_chunk_completion_can_retry_without_duplicate_bytes(
    mock_stage, mock_grading, client, auth_headers, tmp_path
):
    payloads = []
    staged_path = tmp_path / "staged.fits"

    def stage(file_path, _campaign_id):
        payloads.append(Path(file_path).read_bytes())
        if len(payloads) == 1:
            return {"success": False, "error": "private backend detail"}
        staged_path.write_bytes(payloads[-1])
        return {
            "success": True,
            "staging_path": str(staged_path),
            "file_size": len(payloads[-1]),
            "checksum": "checksum",
        }

    mock_stage.side_effect = stage
    create = client.post("/api/v1/uploads?campaign_id=camp1", headers=auth_headers).get_json()
    upload_id = create["upload_id"]
    chunk_b64 = base64.b64encode(b"fits-file-bytes").decode("ascii")
    client.post(
        "/api/v1/uploads/chunks",
        json={"upload_id": upload_id, "chunk_index": 0, "chunk_data_base64": chunk_b64},
        headers=auth_headers,
    )

    failed = client.post(
        "/api/v1/uploads/complete",
        json={"upload_id": upload_id, "total_chunks": 1},
        headers=auth_headers,
    )
    retried = client.post(
        "/api/v1/uploads/complete",
        json={"upload_id": upload_id, "total_chunks": 1},
        headers=auth_headers,
    )

    assert failed.status_code == 500
    assert "private backend detail" not in failed.get_json()["message"]
    assert retried.status_code == 200
    assert payloads == [b"fits-file-bytes", b"fits-file-bytes"]
    mock_grading.assert_called_once()


def test_chunked_completion_missing_chunks_returns_400(client, auth_headers):
    create = client.post("/api/v1/uploads?campaign_id=camp1", headers=auth_headers).get_json()
    upload_id = create["upload_id"]

    resp = client.post(
        "/api/v1/uploads/complete",
        json={"upload_id": upload_id, "total_chunks": 3},
        headers=auth_headers,
    )
    assert resp.status_code == 400
    assert "Not all chunks received" in resp.get_json()["message"]


def test_chunked_completion_unknown_session_returns_404(client, auth_headers):
    resp = client.post(
        "/api/v1/uploads/complete",
        json={"upload_id": 424242, "total_chunks": 1},
        headers=auth_headers,
    )
    assert resp.status_code == 404


def test_chunked_completion_rejects_non_positive_total_chunks(client, auth_headers):
    create = client.post("/api/v1/uploads?campaign_id=camp1", headers=auth_headers).get_json()
    upload_id = create["upload_id"]

    for total_chunks in (0, -1):
        resp = client.post(
            "/api/v1/uploads/complete",
            json={"upload_id": upload_id, "total_chunks": total_chunks},
            headers=auth_headers,
        )
        assert resp.status_code == 400
        assert resp.get_json()["message"] == "total_chunks must be positive"
