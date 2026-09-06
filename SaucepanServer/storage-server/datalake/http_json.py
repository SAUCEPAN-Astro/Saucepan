"""Small stdlib-only helper for datalake HTTP JSON requests."""

from __future__ import annotations

import json
import typing
import urllib.error
import urllib.request


HTTPErrorFormatter = typing.Callable[[urllib.error.HTTPError, str], str]


def request_json(
    method: str,
    url: str,
    body: dict[str, typing.Any] | None = None,
    *,
    token: str = "",
    timeout: int = 120,
    format_http_error: HTTPErrorFormatter | None = None,
) -> dict[str, typing.Any]:
    """Make a JSON request and return its JSON object response.

    An empty response body is represented as an empty object. Callers may
    provide an HTTP error formatter to preserve their existing error text.
    """
    data = None if body is None else json.dumps(body).encode("utf-8")
    req = urllib.request.Request(url, data=data, method=method)
    req.add_header("Content-Type", "application/json")
    if token:
        req.add_header("Authorization", f"Bearer {token}")
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            raw = resp.read().decode("utf-8")
            return json.loads(raw) if raw else {}
    except urllib.error.HTTPError as exc:
        detail = exc.read().decode("utf-8", errors="replace")
        if format_http_error is None:
            message = f"HTTP {exc.code} request failed"
        else:
            message = format_http_error(exc, detail)
        raise RuntimeError(message) from exc
