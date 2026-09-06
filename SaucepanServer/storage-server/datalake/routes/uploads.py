"""Backward compat — use routes.upload."""

from routes.upload import uploads_bp
from routes.upload.grading import (
    _run_post_upload_grading,
    _run_post_upload_grading_for_upload,
    run_post_upload_grading,
    run_post_upload_grading_for_upload,
)
from routes.upload.staging import _finalize_catalog_upload, finalize_catalog_upload

__all__ = [
    "uploads_bp",
    "_run_post_upload_grading",
    "_run_post_upload_grading_for_upload",
    "run_post_upload_grading",
    "run_post_upload_grading_for_upload",
    "_finalize_catalog_upload",
    "finalize_catalog_upload",
]
