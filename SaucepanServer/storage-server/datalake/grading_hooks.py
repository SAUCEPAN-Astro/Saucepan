"""Upload-complete grading hook — Agent 2 fills sync inline grading here."""

from __future__ import annotations

import logging
from typing import Any, Callable

logger = logging.getLogger(__name__)

# Callable signature: on_upload_complete(upload_id: str) -> str (pipeline status)
_on_upload_complete: Callable[[str], str] | None = None


def register_on_upload_complete(fn: Callable[[str], str]) -> None:
    """Register the grading hook (called from routes.upload at import time)."""
    global _on_upload_complete
    _on_upload_complete = fn


def on_upload_complete(upload_id: str) -> str:
    """
    Invoke post-upload grading for a catalog upload_id.

    Default implementation delegates to routes.upload.grading.run_post_upload_grading_for_upload.
    Agent 2 may replace via register_on_upload_complete().
    """
    if _on_upload_complete is not None:
        return _on_upload_complete(upload_id)

    from routes.upload.grading import run_post_upload_grading_for_upload

    return run_post_upload_grading_for_upload(upload_id)


def grading_result_to_dict(status: str) -> dict[str, Any]:
    return {"pipeline_status": status}
