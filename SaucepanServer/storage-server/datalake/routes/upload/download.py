"""Presigned download URLs for catalog frames."""

from __future__ import annotations

import logging

from catalog import Frame, Upload
from db import session_scope
from flask import jsonify
from storage.factory import get_storage_backend

from .bp import PRESIGN_EXPIRES_SECONDS, uploads_bp

logger = logging.getLogger(__name__)


@uploads_bp.route("/frames/<frame_id>/download", methods=["GET"])
def download_frame(frame_id: str):
    """Return a presigned GET URL for a catalog frame's object-store key."""
    backend = get_storage_backend()
    with session_scope() as session:
        frame = session.get(Frame, frame_id)
        if frame is None:
            return jsonify({"success": False, "message": "Frame not found"}), 404
        upload = session.get(Upload, frame.upload_id)
        if upload is None:
            return jsonify({"success": False, "message": "Upload not found"}), 404
        bucket = upload.bucket
        object_key = frame.object_key or upload.object_key

    if not backend.supports_client_download:
        return jsonify(
            {
                "success": False,
                "message": "Client download is not available for the configured storage backend",
            }
        ), 501

    try:
        presigned_url = backend.presign_download(
            bucket,
            object_key,
            expires_seconds=PRESIGN_EXPIRES_SECONDS,
        )
    except Exception:
        logger.exception("Presign download failed for frame %s", frame_id)
        return jsonify({"success": False, "message": "Presign download failed"}), 503

    return jsonify(
        {
            "success": True,
            "frame_id": frame_id,
            "presigned_url": presigned_url,
            "bucket": bucket,
            "object_key": object_key,
            "expires_in": PRESIGN_EXPIRES_SECONDS,
        }
    ), 200
