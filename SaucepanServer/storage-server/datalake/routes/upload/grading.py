"""Post-upload grading via compute-server HTTP API."""

from __future__ import annotations

import logging
import os
from datetime import datetime, timezone
from typing import Any, Dict, Optional

from catalog import Frame, Upload
from compute_client import compute_url, request_grade
from db import session_scope

logger = logging.getLogger(__name__)


def _apply_grade_to_frame(
    session,
    frame_id: str,
    pipeline_status: str,
    grade_result: Optional[Dict[str, Any]],
    ingest_status: Optional[str],
) -> None:
    """Persist inline grade metadata on the catalog ``frames`` row."""
    frame = session.get(Frame, frame_id)
    if frame is None:
        return
    frame.grade_status = pipeline_status
    if grade_result:
        frame.headline_grade = grade_result.get("headline")
        frame.grade_json = grade_result
    if ingest_status is not None:
        frame.ingest_status = ingest_status
    session.add(frame)


def run_post_upload_grading(
    staged_path: str, metadata: Optional[Dict[str, Any]] = None
) -> tuple[str, Optional[Dict[str, Any]], Optional[str]]:
    """
    Post-upload grading: sync inline via compute-server (default).

    Returns (pipeline_status, grade_result_or_none, ingest_status_or_none).
    """
    metadata = dict(metadata or {})
    metadata.setdefault("upload_completed_at", datetime.now(timezone.utc).isoformat())

    task_context = {
        k: metadata.get(k)
        for k in (
            "upload_id",
            "task_id",
            "telescope_id",
            "assignment_sent_at",
            "upload_completed_at",
            "integration_time_requested",
            "filter_requested",
            "predicted_psf_arcsec",
            "idempotency_key",
            "allow_emulator",
            "telescope_is_emulator",
        )
        if metadata.get(k) is not None
    }

    if not compute_url():
        return "compute_unconfigured", None, None

    try:
        update_fits = os.environ.get("GRADE_UPDATE_FITS", "true").lower() not in (
            "0",
            "false",
            "no",
        )
        grade_result, ingest_status = request_grade(
            staged_path,
            task_context,
            update_fits=update_fits,
            post_ingest=True,
        )
        if ingest_status == "success":
            return "grade_assessed_and_ingested", grade_result, "success"
        if ingest_status == "failed":
            return "grade_assessed", grade_result, "failed"
        return "grade_assessed", grade_result, ingest_status
    except Exception:
        logger.exception("compute grade failed")
        return "grade_error", None, None


def run_post_upload_grading_for_upload(upload_id: str) -> str:
    """Load staged frame for upload_id and run inline grading."""
    grade_result: Optional[Dict[str, Any]] = None
    staged_path: Optional[str] = None
    catalog_meta: Dict[str, Any] = {}
    pipeline_status = "upload_not_found"

    with session_scope() as session:
        upload = session.get(Upload, upload_id)
        if upload is None:
            return "upload_not_found"
        frame = (
            session.query(Frame)
            .filter(Frame.upload_id == upload_id)
            .order_by(Frame.created_at.desc())
            .first()
        )
        if frame is None or not frame.staged_path:
            return "frame_not_staged"

        metadata = dict(upload.metadata_json or {})
        metadata.setdefault("upload_id", upload_id)
        metadata.setdefault("task_id", upload.task_id)
        metadata.setdefault("telescope_id", upload.telescope_id)
        metadata.setdefault("s3_key", upload.object_key)
        metadata.setdefault("allow_emulator", (upload.metadata_json or {}).get("allow_emulator"))
        metadata.setdefault(
            "telescope_is_emulator",
            (upload.metadata_json or {}).get("telescope_is_emulator"),
        )
        metadata["frame_id"] = frame.id
        metadata["campaign_id"] = upload.campaign_id

        pipeline_status, grade_result, ingest_status = run_post_upload_grading(
            frame.staged_path, metadata
        )
        _apply_grade_to_frame(session, frame.id, pipeline_status, grade_result, ingest_status)
        staged_path = frame.staged_path
        catalog_meta = {
            "upload_id": upload_id,
            "frame_id": frame.id,
            "telescope_id": upload.telescope_id,
            "task_id": upload.task_id,
            "campaign_id": upload.campaign_id,
            "object_key": upload.object_key or frame.object_key,
            "checksum_sha256": frame.checksum_sha256,
        }

    if grade_result and staged_path:
        try:
            _upsert_cold_frame_catalog(staged_path, grade_result, catalog_meta)
        except Exception:
            logger.exception("frame_catalog upsert failed upload_id=%s (non-fatal)", upload_id)
    return pipeline_status


def _upsert_cold_frame_catalog(
    staged_path: str,
    grade_result: Dict[str, Any],
    meta: Dict[str, Any],
) -> None:
    """Write denormalized L1 index row on cold SQLite after grade."""
    import sys
    from pathlib import Path

    compute_root = Path(__file__).resolve().parents[4] / "compute-server"
    if (compute_root / "grading").is_dir() and str(compute_root) not in sys.path:
        sys.path.insert(0, str(compute_root))

    from grading.catalog_extract import extract_from_fits
    from storage.frame_catalog import upsert_frame_catalog

    row = extract_from_fits(
        staged_path,
        grade=grade_result,
        upload_id=meta.get("upload_id"),
        frame_id=meta.get("frame_id"),
        telescope_id=meta.get("telescope_id") or grade_result.get("telescope_id"),
        task_id=meta.get("task_id") or grade_result.get("task_id"),
        campaign_id=meta.get("campaign_id") or grade_result.get("campaign_id"),
        object_key=meta.get("object_key") or grade_result.get("object_key"),
        checksum_sha256=meta.get("checksum_sha256"),
    )
    upsert_frame_catalog(row)


# Backward-compat aliases for tests and shims
_run_post_upload_grading = run_post_upload_grading
_run_post_upload_grading_for_upload = run_post_upload_grading_for_upload
