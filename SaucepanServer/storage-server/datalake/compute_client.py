"""HTTP client for the compute-server microservice."""

from __future__ import annotations

import logging
import os
import typing

import http_json

logger = logging.getLogger(__name__)


def compute_url() -> str | None:
    url = os.environ.get("COMPUTE_URL", "").strip().rstrip("/")
    return url or None


def _request(
    method: str,
    path: str,
    body: dict[str, typing.Any] | None = None,
    *,
    timeout: int = 120,
) -> dict[str, typing.Any]:
    base = compute_url()
    if not base:
        raise RuntimeError("COMPUTE_URL not set")

    url = f"{base}{path}"
    token = os.environ.get("COMPUTE_TOKEN", "").strip()
    return http_json.request_json(
        method,
        url,
        body or {},
        token=token,
        timeout=timeout,
        format_http_error=lambda exc, detail: f"compute {path} HTTP {exc.code}: {detail}",
    )


def request_grade(
    staged_path: str,
    task_context: dict[str, typing.Any],
    *,
    update_fits: bool = True,
    post_ingest: bool = True,
) -> tuple[dict[str, typing.Any], str | None]:
    """
    POST /v1/grade on compute-server.

    ``post_ingest`` is accepted for backward compatibility but ignored by compute;
    ingest is gated by compute env ``COMPUTE_POST_INGEST=1``.

    Returns (grade_result, ingest_status).
    """
    payload = _request(
        "POST",
        "/v1/grade",
        {
            "staged_path": staged_path,
            "task_context": task_context,
            "update_fits": update_fits,
            # Kept in the body for older compute images; current servers ignore it.
            "post_ingest": post_ingest,
        },
    )
    return payload.get("grade") or {}, payload.get("ingest_status")


def request_photometry(
    staged_path: str,
    task_context: dict[str, typing.Any],
    *,
    update_fits: bool = True,
    run_lp: bool = True,
) -> dict[str, typing.Any]:
    """POST /v1/photometry on compute-server (fail-open caller)."""
    payload = _request(
        "POST",
        "/v1/photometry",
        {
            "staged_path": staged_path,
            "task_context": task_context,
            "update_fits": update_fits,
            "run_lp": run_lp,
            "defer": False,
        },
        timeout=300,
    )
    return payload.get("summary") or payload


def request_stack(frame_paths: list[str], output_path: str) -> dict[str, typing.Any]:
    """POST /v1/stack on compute-server."""
    payload = _request(
        "POST",
        "/v1/stack",
        {"frame_paths": frame_paths, "output_path": output_path},
    )
    try:
        from metrics_hook import notify_stack_complete

        notify_stack_complete(
            payload.get("summary") or {},
            payload.get("output_path") or output_path,
        )
    except Exception:
        logger.exception("metrics stack notify failed for %s", output_path)
    return payload
