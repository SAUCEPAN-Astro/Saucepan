"""Unit tests for compute_client.py — HTTP client to the compute-server microservice.

Never hits real network: urllib.request.urlopen is patched out.
"""

from __future__ import annotations

import io
import json
import urllib.error
from unittest.mock import MagicMock, patch

import pytest

import compute_client


def test_compute_url_unset_returns_none(monkeypatch):
    monkeypatch.delenv("COMPUTE_URL", raising=False)
    assert compute_client.compute_url() is None


def test_compute_url_strips_whitespace_and_trailing_slash(monkeypatch):
    monkeypatch.setenv("COMPUTE_URL", " http://compute.test/ ")
    assert compute_client.compute_url() == "http://compute.test"


def test_compute_url_blank_string_is_none(monkeypatch):
    monkeypatch.setenv("COMPUTE_URL", "   ")
    assert compute_client.compute_url() is None


def _fake_response(payload: dict):
    ctx = MagicMock()
    ctx.__enter__.return_value = ctx
    ctx.read.return_value = json.dumps(payload).encode("utf-8")
    return ctx


def _fake_raw_response(body: bytes):
    ctx = MagicMock()
    ctx.__enter__.return_value = ctx
    ctx.read.return_value = body
    return ctx


def test_request_raises_when_compute_url_unset(monkeypatch):
    monkeypatch.delenv("COMPUTE_URL", raising=False)
    with pytest.raises(RuntimeError, match="COMPUTE_URL not set"):
        compute_client._request("POST", "/v1/grade", {})


@patch("http_json.urllib.request.urlopen")
def test_request_grade_success(mock_urlopen, monkeypatch):
    monkeypatch.setenv("COMPUTE_URL", "http://compute.test")
    mock_urlopen.return_value = _fake_response(
        {"grade": {"headline": 85}, "ingest_status": "success"}
    )
    grade, ingest_status = compute_client.request_grade("/staged/a.fits", {"task_id": "1"})
    assert grade == {"headline": 85}
    assert ingest_status == "success"


@patch("http_json.urllib.request.urlopen")
def test_request_grade_missing_grade_key_defaults_empty_dict(mock_urlopen, monkeypatch):
    monkeypatch.setenv("COMPUTE_URL", "http://compute.test")
    mock_urlopen.return_value = _fake_response({})
    grade, ingest_status = compute_client.request_grade("/staged/a.fits", {})
    assert grade == {}
    assert ingest_status is None


@patch("http_json.urllib.request.urlopen")
def test_request_empty_response_preserves_empty_object_behavior(mock_urlopen, monkeypatch):
    monkeypatch.setenv("COMPUTE_URL", "http://compute.test")
    mock_urlopen.return_value = _fake_raw_response(b"")

    assert compute_client._request("POST", "/v1/grade", None) == {}
    request = mock_urlopen.call_args.args[0]
    assert json.loads(request.data) == {}
    assert mock_urlopen.call_args.kwargs["timeout"] == 120


@patch("http_json.urllib.request.urlopen")
def test_request_adds_bearer_token_when_set(mock_urlopen, monkeypatch):
    monkeypatch.setenv("COMPUTE_URL", "http://compute.test")
    monkeypatch.setenv("COMPUTE_TOKEN", "tok123")
    mock_urlopen.return_value = _fake_response({"ok": True})
    compute_client._request("POST", "/v1/grade", {})
    request_obj = mock_urlopen.call_args[0][0]
    assert request_obj.get_header("Authorization") == "Bearer tok123"


@patch("http_json.urllib.request.urlopen")
def test_request_http_error_raises_runtime_error(mock_urlopen, monkeypatch):
    monkeypatch.setenv("COMPUTE_URL", "http://compute.test")
    err = urllib.error.HTTPError(
        url="http://compute.test/v1/grade",
        code=500,
        msg="Internal Error",
        hdrs=None,
        fp=io.BytesIO(b"boom"),
    )
    mock_urlopen.side_effect = err
    with pytest.raises(RuntimeError, match="compute /v1/grade HTTP 500"):
        compute_client._request("POST", "/v1/grade", {})


@patch("http_json.urllib.request.urlopen")
def test_request_photometry_returns_summary(mock_urlopen, monkeypatch):
    monkeypatch.setenv("COMPUTE_URL", "http://compute.test")
    mock_urlopen.return_value = _fake_response({"summary": {"snr": 12.3}})
    result = compute_client.request_photometry("/staged/a.fits", {})
    assert result == {"snr": 12.3}
    assert mock_urlopen.call_args.kwargs["timeout"] == 300


@patch("http_json.urllib.request.urlopen")
def test_request_photometry_falls_back_to_full_payload(mock_urlopen, monkeypatch):
    monkeypatch.setenv("COMPUTE_URL", "http://compute.test")
    mock_urlopen.return_value = _fake_response({"other": "data"})
    result = compute_client.request_photometry("/staged/a.fits", {})
    assert result == {"other": "data"}


@patch("http_json.urllib.request.urlopen")
def test_request_stack_notifies_metrics_hook(mock_urlopen, monkeypatch):
    monkeypatch.setenv("COMPUTE_URL", "http://compute.test")
    mock_urlopen.return_value = _fake_response(
        {"summary": {"stack_snr": 5.0}, "output_path": "/out/stack.fits"}
    )
    with patch("metrics_hook.notify_stack_complete") as mock_notify:
        result = compute_client.request_stack(["/a.fits", "/b.fits"], "/out/stack.fits")
    assert result["summary"]["stack_snr"] == 5.0
    mock_notify.assert_called_once_with({"stack_snr": 5.0}, "/out/stack.fits")


@patch("http_json.urllib.request.urlopen")
def test_request_stack_notify_failure_is_fail_open(mock_urlopen, monkeypatch):
    """metrics_hook failure must not break request_stack's return value."""
    monkeypatch.setenv("COMPUTE_URL", "http://compute.test")
    mock_urlopen.return_value = _fake_response({"summary": {}, "output_path": "/out.fits"})
    with patch("metrics_hook.notify_stack_complete", side_effect=RuntimeError("boom")):
        result = compute_client.request_stack(["/a.fits"], "/out.fits")
    assert result["output_path"] == "/out.fits"
