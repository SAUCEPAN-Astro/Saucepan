"""Presigned object-store upload and completion routes."""

from __future__ import annotations

import logging
import shutil
import uuid
from pathlib import Path
from typing import Any

from catalog import Upload
from db import session_scope
from flask import jsonify, request
from sqlalchemy import select
from storage.factory import get_storage_backend

from .bp import PRESIGN_EXPIRES_SECONDS, uploads_bp
from .grading import run_post_upload_grading
from .sessions import cleanup_upload_session, upload_sessions
from .staging import (
    cleanup_staged_file,
    finalize_catalog_upload,
    object_key_for_upload,
    ObjectNotFoundError,
    storage_client,
)

logger = logging.getLogger(__name__)


@uploads_bp.route("/uploads/presign", methods=["POST"])
def presign_upload():
    """Create catalog upload row and return a presigned PUT URL."""
    data = request.get_json(silent=True) or {}
    filename = data.get("filename")
    campaign_id = data.get("campaign_id")
    if not filename or not campaign_id:
        return jsonify({"success": False, "message": "filename and campaign_id are required"}), 400

    backend = get_storage_backend()
    upload_id = str(uuid.uuid4())
    bucket = backend.bucket_for_tier("default")
    try:
        object_key = object_key_for_upload(
            str(campaign_id),
            str(data["task_id"]) if data.get("task_id") is not None else None,
            upload_id,
            filename,
        )
    except ValueError as exc:
        return jsonify({"success": False, "message": str(exc)}), 400
    content_type = data.get("content_type") or "application/fits"

    upload = Upload(
        id=upload_id,
        status="pending",
        bucket=bucket,
        object_key=object_key,
        filename=Path(filename).name,
        campaign_id=str(campaign_id),
        task_id=str(data["task_id"]) if data.get("task_id") is not None else None,
        telescope_id=data.get("telescope_id"),
        content_type=content_type,
        metadata_json={
            k: data[k]
            for k in (
                "assignment_sent_at",
                "integration_time_requested",
                "filter_requested",
                "predicted_psf_arcsec",
                "idempotency_key",
                "clock_source",
                "detector_temp_c",
            )
            if data.get(k) is not None
        },
    )

    try:
        with session_scope() as session:
            session.add(upload)
        presigned_url = backend.presign_upload(
            bucket,
            object_key,
            expires_seconds=PRESIGN_EXPIRES_SECONDS,
            content_type=content_type,
        )
    except Exception:
        logger.exception("Presign failed for %s", object_key)
        return jsonify({"success": False, "message": "Presign failed"}), 503

    return jsonify(
        {
            "success": True,
            "upload_id": upload_id,
            "presigned_url": presigned_url,
            "object_key": object_key,
            "bucket": bucket,
            "expires_in": PRESIGN_EXPIRES_SECONDS,
        }
    ), 201


@uploads_bp.route("/uploads/complete", methods=["POST"])
def complete_catalog_upload():
    """Complete presigned upload or legacy chunk session."""
    data = request.get_json(silent=True) or {}
    upload_id = data.get("upload_id")

    if upload_id is not None and not isinstance(upload_id, int):
        upload_id_str = str(upload_id)
        upload_status = None
        completed_object_key = None
        with session_scope() as session:
            upload = session.execute(
                select(Upload).where(Upload.id == upload_id_str).with_for_update()
            ).scalar_one_or_none()
            if upload is None:
                return jsonify({"success": False, "message": "Upload not found"}), 404
            upload_status = upload.status
            if upload_status == "completed":
                completed_object_key = upload.object_key
            elif upload_status != "pending":
                return jsonify(
                    {"success": False, "message": "Upload is already being completed"}
                ), 409
            else:
                upload.status = "processing"
        if upload_status == "completed":
            return jsonify(
                {
                    "success": True,
                    "upload_id": upload_id_str,
                    "message": "Upload already completed",
                    "object_key": completed_object_key,
                }
            ), 200
        try:
            result = finalize_catalog_upload(upload_id_str, extra_metadata=_completion_metadata(data))
        except ObjectNotFoundError as exc:
            logger.warning("Complete upload unavailable for %s: %s", upload_id_str, exc)
            _reset_processing_upload(upload_id_str)
            return jsonify({"success": False, "message": "Object not found"}), 404
        except Exception:
            logger.exception("Complete upload failed for %s", upload_id_str)
            _reset_processing_upload(upload_id_str)
            return jsonify({"success": False, "message": "Complete failed"}), 502

        return jsonify({**result, "message": "Upload completed"}), 202

    return _complete_chunk_upload(data)


_COMPLETION_METADATA_KEYS = frozenset(
    {
        "assignment_sent_at",
        "integration_time_requested",
        "filter_requested",
        "predicted_psf_arcsec",
        "idempotency_key",
        "clock_source",
        "detector_temp_c",
        "date_obs",
        "task_snapshot",
        "timing",
        "campaign_comp_stars",
        "telescope_snapshot",
        "node_cache",
        "reputation_stats",
        "allow_emulator",
        "telescope_is_emulator",
    }
)

_PRIVATE_METADATA_KEY_NAMES = frozenset(
    {
        "observer",
        "user_id",
        "userid",
        "username",
        "researcher",
        "researcher_id",
        "researcherid",
        "display_name",
        "operator",
        "operator_id",
        "operatorid",
        "owner",
        "owner_id",
        "ownerid",
        "contact",
        "author",
        "author_id",
        "authorid",
        "account",
        "account_id",
        "accountid",
        "principal",
        "name",
        "full_name",
        "first_name",
        "last_name",
        "researcher_name",
        "operator_name",
        "owner_name",
        "contact_name",
    }
)
_PRIVATE_METADATA_KEY_COMPACT = frozenset(
    name.replace("_", "") for name in _PRIVATE_METADATA_KEY_NAMES
)


def _is_private_metadata_key(key: object) -> bool:
    normalized = str(key).strip().lower().replace("-", "_")
    compact = normalized.replace("_", "")
    return (
        compact in _PRIVATE_METADATA_KEY_COMPACT
        or "email" in compact
        or compact.endswith(("username", "userid", "name"))
    )


def _sanitize_metadata_value(value: Any) -> Any:
    """Remove identity-bearing keys from nested machine metadata."""
    if isinstance(value, dict):
        return {
            key: _sanitize_metadata_value(nested)
            for key, nested in value.items()
            if not _is_private_metadata_key(key)
        }
    if isinstance(value, list):
        return [_sanitize_metadata_value(item) for item in value]
    return value


def _completion_metadata(data: dict[str, Any]) -> dict[str, Any]:
    """Keep completion metadata machine-scoped and reject arbitrary user data."""
    return {
        key: _sanitize_metadata_value(data[key])
        for key in _COMPLETION_METADATA_KEYS
        if data.get(key) is not None
    }


def _reset_processing_upload(upload_id: str) -> None:
    """Allow a failed completion to be retried without exposing DB details."""
    try:
        with session_scope() as session:
            upload = session.get(Upload, upload_id)
            if upload is not None and upload.status == "processing":
                upload.status = "pending"
    except Exception:
        logger.exception("could not reset failed upload %s", upload_id)


def _complete_chunk_upload(data: dict[str, Any]):
    """Legacy chunked upload completion (in-memory sessions)."""
    if not data:
        return jsonify({"success": False, "message": "JSON body required"}), 400

    upload_id = data.get("upload_id")
    total_chunks = data.get("total_chunks", 0)

    if upload_id not in upload_sessions:
        return jsonify({"success": False, "message": "Upload session not found"}), 404

    session = upload_sessions[upload_id]
    temp_dir = Path(session["temp_dir"])

    try:
        if isinstance(total_chunks, bool) or not isinstance(total_chunks, int) or total_chunks <= 0:
            return jsonify({"success": False, "message": "total_chunks must be positive"}), 400
        received_chunks = list(session["chunks"].keys())
        expected = list(range(total_chunks))
        if sorted(expected) != sorted(received_chunks):
            return jsonify(
                {
                    "success": False,
                    "message": f"Not all chunks received: got {len(received_chunks)}/{total_chunks}",
                }
            ), 400

        final_file = temp_dir / "final_file"
        with final_file.open("wb") as dst:
            for ci in sorted(session["chunks"].keys()):
                chunk_path = Path(session["chunks"][ci]["path"])
                with chunk_path.open("rb") as src:
                    shutil.copyfileobj(src, dst)
        final_file.chmod(0o600)

        result = storage_client.upload_to_staging(str(final_file), session["campaign_id"])
        if not result.get("success"):
            logger.error("chunk upload staging failed upload_id=%s", upload_id)
            return jsonify({"success": False, "message": "Staging failed"}), 500

        staged_path = result["staging_path"]
        grade_metadata = {
            "upload_id": str(upload_id),
            "task_id": data.get("task_id"),
            "telescope_id": data.get("telescope_id"),
            "assignment_sent_at": data.get("assignment_sent_at"),
            "integration_time_requested": data.get("integration_time_requested"),
            "filter_requested": data.get("filter_requested"),
            "predicted_psf_arcsec": data.get("predicted_psf_arcsec"),
        }
        try:
            pipeline_status, _, _ = run_post_upload_grading(staged_path, grade_metadata)
        finally:
            cleanup_staged_file(staged_path)

        session["status"] = "staged"
        cleanup_upload_session(upload_id)

        return jsonify(
            {
                "success": True,
                "upload_id": upload_id,
                "message": "Upload completed",
                "file_path": None,
                "checksum": result.get("checksum", ""),
                "file_size": result.get("file_size", 0),
                "pipeline_status": pipeline_status,
            }
        )

    except Exception:
        logger.exception("failed to complete chunked upload %s", upload_id)
        return jsonify({"success": False, "message": "Failed to complete upload"}), 500
