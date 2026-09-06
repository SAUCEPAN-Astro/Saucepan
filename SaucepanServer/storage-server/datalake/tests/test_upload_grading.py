"""Unit tests for routes/upload/grading.py — post-upload grading orchestration."""

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


def test_run_post_upload_grading_no_compute_url(monkeypatch):
    from routes.upload.grading import run_post_upload_grading

    monkeypatch.delenv("PIPELINE_MODE", raising=False)
    monkeypatch.delenv("COMPUTE_URL", raising=False)

    status, grade, ingest = run_post_upload_grading("/staged/a.fits", {})
    assert status == "compute_unconfigured"
    assert grade is None
    assert ingest is None


@patch("routes.upload.grading.request_grade")
def test_run_post_upload_grading_success_ingested(mock_request_grade, monkeypatch):
    from routes.upload.grading import run_post_upload_grading

    monkeypatch.setenv("COMPUTE_URL", "http://compute.test")
    mock_request_grade.return_value = ({"headline": 90}, "success")

    status, grade, ingest = run_post_upload_grading("/staged/a.fits", {"upload_id": "u1"})
    assert status == "grade_assessed_and_ingested"
    assert grade == {"headline": 90}
    assert ingest == "success"


@patch("routes.upload.grading.request_grade")
def test_run_post_upload_grading_ingest_failed(mock_request_grade, monkeypatch):
    from routes.upload.grading import run_post_upload_grading

    monkeypatch.setenv("COMPUTE_URL", "http://compute.test")
    mock_request_grade.return_value = ({"headline": 10}, "failed")

    status, grade, ingest = run_post_upload_grading("/staged/a.fits", {})
    assert status == "grade_assessed"
    assert ingest == "failed"


@patch("routes.upload.grading.request_grade", side_effect=RuntimeError("compute down"))
def test_run_post_upload_grading_compute_error(mock_request_grade, monkeypatch):
    from routes.upload.grading import run_post_upload_grading

    monkeypatch.setenv("COMPUTE_URL", "http://compute.test")
    status, grade, ingest = run_post_upload_grading("/staged/a.fits", {})
    assert status == "grade_error"
    assert grade is None
    assert ingest is None


def test_run_post_upload_grading_for_upload_missing_upload(catalog_db):
    from routes.upload.grading import run_post_upload_grading_for_upload

    result = run_post_upload_grading_for_upload("does-not-exist")
    assert result == "upload_not_found"


def test_run_post_upload_grading_for_upload_frame_not_staged(catalog_db):
    from catalog import Upload
    from db import session_scope
    from routes.upload.grading import run_post_upload_grading_for_upload

    with session_scope() as session:
        session.add(
            Upload(
                id="u1",
                status="pending",
                bucket="b",
                object_key="k",
                filename="a.fits",
                campaign_id="camp1",
            )
        )
    result = run_post_upload_grading_for_upload("u1")
    assert result == "frame_not_staged"


@patch("routes.upload.grading._upsert_cold_frame_catalog")
@patch("routes.upload.grading.request_grade")
def test_run_post_upload_grading_for_upload_success(
    mock_request_grade, mock_upsert_catalog, catalog_db, monkeypatch
):
    from catalog import Frame, Upload
    from db import session_scope
    from routes.upload.grading import run_post_upload_grading_for_upload

    monkeypatch.setenv("COMPUTE_URL", "http://compute.test")
    mock_request_grade.return_value = ({"headline": 77}, "success")

    with session_scope() as session:
        session.add(
            Upload(
                id="u1",
                status="completed",
                bucket="b",
                object_key="k",
                filename="a.fits",
                campaign_id="camp1",
            )
        )
        session.add(
            Frame(
                id="f1",
                upload_id="u1",
                campaign_id="camp1",
                object_key="k",
                staged_path="/staged/a.fits",
            )
        )

    status = run_post_upload_grading_for_upload("u1")
    assert status == "grade_assessed_and_ingested"

    with session_scope() as session:
        frame = session.get(Frame, "f1")
        assert frame.grade_status == "grade_assessed_and_ingested"
        assert frame.headline_grade == 77
        assert frame.ingest_status == "success"

    mock_upsert_catalog.assert_called_once()


@patch(
    "routes.upload.grading._upsert_cold_frame_catalog",
    side_effect=RuntimeError("catalog boom"),
)
@patch("routes.upload.grading.request_grade")
def test_run_post_upload_grading_for_upload_catalog_upsert_failure_is_non_fatal(
    mock_request_grade, mock_upsert_catalog, catalog_db, monkeypatch
):
    """A frame_catalog upsert failure must not prevent the pipeline_status from returning."""
    from catalog import Frame, Upload
    from db import session_scope
    from routes.upload.grading import run_post_upload_grading_for_upload

    monkeypatch.setenv("COMPUTE_URL", "http://compute.test")
    mock_request_grade.return_value = ({"headline": 50}, "success")

    with session_scope() as session:
        session.add(
            Upload(
                id="u1",
                status="completed",
                bucket="b",
                object_key="k",
                filename="a.fits",
                campaign_id="camp1",
            )
        )
        session.add(
            Frame(
                id="f1",
                upload_id="u1",
                campaign_id="camp1",
                object_key="k",
                staged_path="/staged/a.fits",
            )
        )

    status = run_post_upload_grading_for_upload("u1")
    assert status == "grade_assessed_and_ingested"
