"""POST graded frames to the task-server grades ingest API."""

from __future__ import annotations

import json
import logging
import os
import typing
import urllib.error
import urllib.request

logger = logging.getLogger(__name__)


def post_grade_to_ingest(grade: dict[str, typing.Any]) -> str | None:
    """
    POST grade JSON to task-server ``POST /api/v1/grades/ingest``.

    Returns response body on success, None if URL unset.
    """
    url = (
        os.environ.get("GRADES_INGEST_URL")
        or os.environ.get("FLASK_GRADES_URL")
        or os.environ.get("GRADES_CALLBACK_URL")
    )
    if not url:
        logger.info("GRADES_INGEST_URL not set; skipping grade callback")
        return None

    payload = json.dumps(grade).encode("utf-8")
    headers = {"Content-Type": "application/json"}
    token = os.environ.get("GRADES_INGEST_TOKEN")
    if token:
        headers["Authorization"] = f"Bearer {token}"

    req = urllib.request.Request(url, data=payload, headers=headers, method="POST")
    for attempt in range(2):
        try:
            with urllib.request.urlopen(req, timeout=10) as resp:
                return resp.read().decode("utf-8")
        except urllib.error.HTTPError as exc:
            if exc.code == 502 and attempt == 0:
                logger.warning("Grades ingest HTTP 502 — retrying once")
                continue
            logger.error("Grades ingest HTTP %s: %s", exc.code, exc.read().decode())
            raise
    return None
