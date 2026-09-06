"""Bearer auth for datalake routes (DATALAKE_GRADING_TOKEN)."""

from __future__ import annotations

import logging
import os
import sys
from functools import wraps
from typing import Any, Callable

from flask import Blueprint, jsonify, request

logger = logging.getLogger(__name__)


def _allow_insecure() -> bool:
    if os.environ.get("DATALAKE_ALLOW_INSECURE", "").strip() == "1":
        return True
    return os.environ.get("FLASK_ENV", "").strip().lower() == "development"


def _expected_token() -> str:
    return os.environ.get("DATALAKE_GRADING_TOKEN", "").strip()


def check_grading_token() -> tuple[Any, int] | None:
    """Return (response, status) on auth failure, else None."""
    expected = _expected_token()
    if not expected:
        if _allow_insecure():
            return None
        return jsonify({"success": False, "message": "DATALAKE_GRADING_TOKEN required"}), 401
    auth = request.headers.get("Authorization", "")
    if auth != f"Bearer {expected}":
        return jsonify({"success": False, "message": "Unauthorized"}), 401
    return None


def require_grading_token(fn: Callable[..., Any]) -> Callable[..., Any]:
    """Decorator requiring Authorization: Bearer <DATALAKE_GRADING_TOKEN>."""

    @wraps(fn)
    def wrapper(*args: Any, **kwargs: Any):
        err = check_grading_token()
        if err:
            return err
        return fn(*args, **kwargs)

    return wrapper


def ensure_grading_token_at_startup() -> None:
    """Fail closed at process start when token unset outside dev/insecure mode."""
    if _expected_token() or _allow_insecure():
        if not _expected_token() and _allow_insecure():
            logger.warning("DATALAKE_ALLOW_INSECURE=1 permits unauthenticated upload routes")
        return
    logger.error("DATALAKE_GRADING_TOKEN is unset; refusing to start")
    sys.exit(1)


def register_upload_auth(uploads_bp: Blueprint) -> None:
    """Protect all routes on uploads_bp with grading token auth."""

    @uploads_bp.before_request
    def _uploads_require_grading_token():
        err = check_grading_token()
        if err:
            return err
