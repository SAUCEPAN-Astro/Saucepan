"""Saucepan Data Lake: upload API package (Flask blueprint)."""

from auth import register_upload_auth

from routes.upload.bp import (  # noqa: F401
    MIN_FRAMES_FOR_STACK,
    PRESIGN_EXPIRES_SECONDS,
    logger,
    uploads_bp,
)

# Register route handlers on uploads_bp
from routes.upload import download  # noqa: E402, F401
from routes.upload import grading  # noqa: E402, F401
from routes.upload import presign  # noqa: E402, F401
from routes.upload import sessions  # noqa: E402, F401
from routes.upload import simple  # noqa: E402, F401

from grading_hooks import register_on_upload_complete
from routes.upload.grading import run_post_upload_grading_for_upload

register_on_upload_complete(run_post_upload_grading_for_upload)
register_upload_auth(uploads_bp)

router = uploads_bp  # legacy alias for main.py
