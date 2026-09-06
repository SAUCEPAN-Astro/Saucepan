"""Chunked upload sessions (in-memory dev path)."""

from __future__ import annotations

import base64
import logging
import os
import shutil
import tempfile
from pathlib import Path
from typing import Any, Dict

from flask import jsonify, request

from .bp import uploads_bp

logger = logging.getLogger(__name__)

upload_sessions: Dict[int, Dict[str, Any]] = {}


def generate_upload_session() -> Dict[str, Any]:
    """Generate a new upload session."""
    session_id = len(upload_sessions) + 1
    upload_sessions[session_id] = {
        "id": session_id,
        "chunks": {},
        "metadata": {},
        "status": "pending",
    }
    return upload_sessions[session_id]


def cleanup_upload_session(upload_id: int) -> None:
    """Clean up upload session temporary files."""
    if upload_id in upload_sessions:
        session = upload_sessions[upload_id]
        if "temp_dir" in session:
            temp_dir = Path(session["temp_dir"])
            if temp_dir.exists():
                shutil.rmtree(temp_dir, ignore_errors=True)


@uploads_bp.route("/uploads", methods=["POST"])
def create_upload_session():
    """Create a new upload session for file uploads."""
    campaign_id = request.args.get("campaign_id")
    if not campaign_id:
        return jsonify({"success": False, "message": "campaign_id is required"}), 400

    dataset_name = request.args.get("dataset_name")

    try:
        session = generate_upload_session()
        temp_dir = Path(tempfile.mkdtemp(prefix=f"upload_{session['id']}_"))
        temp_dir.chmod(0o700)

        session["temp_dir"] = str(temp_dir)
        session["campaign_id"] = campaign_id
        session["dataset_name"] = dataset_name
        session["status"] = "uploading"

        return jsonify(
            {
                "success": True,
                "upload_id": session["id"],
                "message": "Upload session created successfully",
            }
        ), 202

    except Exception:
        logger.exception("failed to create upload session")
        return jsonify(
            {"success": False, "message": "Failed to create upload session"}
        ), 500


@uploads_bp.route("/uploads/chunks", methods=["POST"])
def upload_chunk():
    """Upload a chunk of data for an ongoing upload session."""
    data = request.get_json(silent=True)
    if not data:
        return jsonify({"success": False, "message": "JSON body required"}), 400

    upload_id = data.get("upload_id")
    chunk_index = data.get("chunk_index")

    if not isinstance(chunk_index, int) or chunk_index < 0:
        return jsonify({"success": False, "message": "chunk_index must be a non-negative integer"}), 400

    if upload_id not in upload_sessions:
        return jsonify({"success": False, "message": "Upload session not found"}), 404

    session = upload_sessions[upload_id]
    temp_dir = Path(session["temp_dir"])

    try:
        chunk_data = base64.b64decode(data.get("chunk_data_base64", ""))
        chunk_size = data.get("chunk_size", len(chunk_data))

        chunk_file = temp_dir / f"chunk_{chunk_index}"
        with open(chunk_file, "wb") as f:
            f.write(chunk_data)
        os.chmod(chunk_file, 0o600)

        session["chunks"][chunk_index] = {
            "path": str(chunk_file),
            "size": chunk_size,
        }

        return jsonify(
            {
                "success": True,
                "upload_id": upload_id,
                "chunk_index": chunk_index,
                "message": "Chunk uploaded successfully",
            }
        )

    except Exception:
        logger.exception("failed to upload chunk upload_id=%s", upload_id)
        return jsonify({"success": False, "message": "Failed to upload chunk"}), 500


@uploads_bp.route("/uploads/sessions/<int:upload_id>", methods=["GET"])
def get_upload_session(upload_id: int):
    """Get upload session details."""
    if upload_id not in upload_sessions:
        return jsonify({"success": False, "message": "Upload session not found"}), 404
    session = upload_sessions[upload_id]
    return jsonify(
        {
            "id": session["id"],
            "status": session["status"],
            "metadata": session.get("metadata", {}),
            "chunks": {
                str(index): {"size": chunk.get("size", 0)}
                for index, chunk in session.get("chunks", {}).items()
            },
        }
    )


@uploads_bp.route("/uploads/sessions/<int:upload_id>", methods=["DELETE"])
def cancel_upload_session(upload_id: int):
    """Cancel an upload session and clean up temp files."""
    if upload_id not in upload_sessions:
        return jsonify({"success": False, "message": "Upload session not found"}), 404

    cleanup_upload_session(upload_id)
    del upload_sessions[upload_id]

    return jsonify({"success": True, "message": "Upload session cancelled"})
