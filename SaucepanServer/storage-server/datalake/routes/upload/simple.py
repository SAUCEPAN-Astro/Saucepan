"""Single-shot multipart upload without chunking."""

from __future__ import annotations

import json
import logging
import uuid
from pathlib import Path
import tempfile

from compute_client import compute_url, request_photometry, request_stack
from catalog import Frame, Upload
from db import session_scope
from flask import jsonify, request
from storage.backend import safe_path_component
from storage.factory import get_storage_backend

from .bp import MIN_FRAMES_FOR_STACK, uploads_bp
from .grading import run_post_upload_grading
from .staging import cleanup_staged_file, storage_client

logger = logging.getLogger(__name__)


def _campaign_wants_stack(form) -> bool:
    """Depth campaigns opt into stack via product.mode=stack (#422). Default: no."""
    raw = form.get("product") or form.get("product_json")
    if raw:
        try:
            prod = json.loads(raw) if isinstance(raw, str) else raw
        except (TypeError, json.JSONDecodeError):
            prod = {}
        if isinstance(prod, dict) and str(prod.get("mode", "")).lower() == "stack":
            return True
    mode = (form.get("product_mode") or "").strip().lower()
    return mode == "stack"


@uploads_bp.route("/uploads/simple-upload", methods=["POST"])
def simple_file_upload():
    """Simple single-file upload endpoint (no chunking)."""
    if "file" not in request.files:
        return jsonify({"success": False, "message": "No file provided"}), 400

    file = request.files["file"]
    campaign_id = request.form.get("campaign_id")

    if not campaign_id:
        return jsonify({"success": False, "message": "campaign_id is required"}), 400
    try:
        safe_campaign = safe_path_component(campaign_id, "campaign_id")
        safe_filename = safe_path_component(Path(file.filename or "upload.fits").name, "filename")
    except ValueError as exc:
        return jsonify({"success": False, "message": str(exc)}), 400

    temp_file = Path(tempfile.gettempdir()) / f"simple_{uuid.uuid4().hex}_{safe_filename}"
    staged_path = None
    upload_id = uuid.uuid4().hex
    try:
        temp_file.touch(mode=0o600, exist_ok=False)
        file.save(str(temp_file))

        result = storage_client.upload_to_staging(str(temp_file), campaign_id)
        if not result.get("success"):
            temp_file.unlink(missing_ok=True)
            return jsonify({"success": False, "message": "Staging failed"}), 500

        temp_file.unlink(missing_ok=True)

        staged_path = result["staging_path"]
        object_key = f"{safe_campaign}/simple/{upload_id}_{safe_filename}"
        content_type = file.mimetype or "application/fits"
        backend = get_storage_backend()
        bucket = backend.bucket_for_tier()
        with Path(staged_path).open("rb") as stream:
            backend.put_object(
                bucket,
                object_key,
                stream,
                content_type=content_type,
                length=Path(staged_path).stat().st_size,
            )
        with session_scope() as session:
            session.add(
                Upload(
                    id=upload_id,
                    status="completed",
                    bucket=bucket,
                    object_key=object_key,
                    filename=safe_filename,
                    campaign_id=safe_campaign,
                    content_type=content_type,
                    size_bytes=result.get("file_size"),
                    metadata_json={"source": "simple_upload"},
                )
            )
            session.add(
                Frame(
                    upload_id=upload_id,
                    campaign_id=safe_campaign,
                    object_key=object_key,
                    staged_path=staged_path,
                    checksum_sha256=result.get("checksum"),
                    size_bytes=result.get("file_size"),
                )
            )
        grade_metadata = {
            "upload_id": upload_id,
            "task_id": request.form.get("task_id"),
            "telescope_id": request.form.get("telescope_id"),
            "assignment_sent_at": request.form.get("assignment_sent_at"),
            "integration_time_requested": request.form.get(
                "integration_time_requested", type=float
            ),
            "filter_requested": request.form.get("filter_requested"),
            "predicted_psf_arcsec": request.form.get("predicted_psf_arcsec", type=float),
        }
        pipeline_status, grade_result, ingest_status = run_post_upload_grading(
            staged_path, grade_metadata
        )
        with session_scope() as session:
            frame = session.query(Frame).filter(Frame.upload_id == upload_id).first()
            if frame is not None:
                frame.grade_status = pipeline_status
                frame.grade_json = grade_result
                frame.ingest_status = ingest_status

        try:
            if compute_url() and not _campaign_wants_stack(request.form):
                # Default time-domain path: photometry table, never force /v1/stack (#422).
                logger.info(
                    "Campaign '%s' product route=photometry — skipping auto-stack",
                    campaign_id,
                )
                try:
                    phot = request_photometry(
                        staged_path,
                        {
                            **grade_metadata,
                            "product": {"mode": request.form.get("product_mode") or "per_frame"},
                        },
                        run_lp=True,
                    )
                    pipeline_status += ", photometry"
                    if phot.get("lp"):
                        pipeline_status += " (lp)"
                except Exception:
                    logger.exception("photometry failed campaign_id=%s", campaign_id)
                    pipeline_status += ", photometry_error"
            elif compute_url() and _campaign_wants_stack(request.form):
                campaign_dir = Path(staged_path).parent
                fits_files = sorted(campaign_dir.glob("*.fits"))
                if len(fits_files) >= MIN_FRAMES_FOR_STACK:
                    logger.info(
                        "Campaign '%s' product.mode=stack with %d frames — /v1/stack",
                        campaign_id,
                        len(fits_files),
                    )
                    output_dir = Path(storage_client.storage_root) / "processed" / campaign_id
                    output_dir.mkdir(parents=True, exist_ok=True)
                    output_path = output_dir / "stacked.fits"
                    try:
                        stack_result = request_stack(
                            [str(f) for f in fits_files],
                            str(output_path),
                        )
                    finally:
                        cleanup_staged_file(output_path)
                    pipeline_status += ", stacked"
                    summary = stack_result.get("summary") or {}
                    if summary:
                        pipeline_status += (
                            f" (snr={summary.get('stack_snr', 0):.1f}:1 "
                            f"from {summary.get('n_frames_used', 0)} frames)"
                        )
        except Exception:
            logger.exception("stack failed campaign_id=%s", campaign_id)
            if isinstance(pipeline_status, str):
                pipeline_status += ", stack_error"

        return jsonify(
            {
                "success": True,
                "message": "File uploaded successfully",
                "upload_id": upload_id,
                "file_path": None,
                "checksum": result.get("checksum", ""),
                "pipeline_status": pipeline_status,
            }
        ), 202

    except Exception:
        logger.exception("simple upload failed")
        return jsonify({"success": False, "message": "Failed to upload file"}), 500
    finally:
        if upload_id:
            try:
                with session_scope() as session:
                    frame = session.query(Frame).filter(Frame.upload_id == upload_id).first()
                    if frame is not None:
                        frame.staged_path = None
            except Exception:
                logger.exception("could not clear simple upload path upload_id=%s", upload_id)
        if staged_path:
            cleanup_staged_file(staged_path)
        temp_file.unlink(missing_ok=True)
