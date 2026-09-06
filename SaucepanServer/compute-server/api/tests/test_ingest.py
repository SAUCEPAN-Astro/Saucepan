"""Tests for grades-ingest HTTP callback (api/ingest.py)."""

from __future__ import annotations

import urllib.error

import pytest
from api.ingest import post_grade_to_ingest


def test_returns_none_when_no_url_configured(monkeypatch):
    monkeypatch.delenv("GRADES_INGEST_URL", raising=False)
    monkeypatch.delenv("FLASK_GRADES_URL", raising=False)
    monkeypatch.delenv("GRADES_CALLBACK_URL", raising=False)

    assert post_grade_to_ingest({"headline": 1}) is None


def test_uses_flask_grades_url_fallback(monkeypatch):
    monkeypatch.delenv("GRADES_INGEST_URL", raising=False)
    monkeypatch.setenv("FLASK_GRADES_URL", "http://example.test/ingest")
    monkeypatch.delenv("GRADES_CALLBACK_URL", raising=False)

    captured = {}

    class _Resp:
        def __enter__(self):
            return self

        def __exit__(self, *a):
            return False

        def read(self):
            return b"ok-body"

    def fake_urlopen(req, timeout=10):
        captured["url"] = req.full_url
        captured["headers"] = dict(req.header_items())
        return _Resp()

    monkeypatch.setattr("urllib.request.urlopen", fake_urlopen)

    result = post_grade_to_ingest({"headline": 1})
    assert result == "ok-body"
    assert captured["url"] == "http://example.test/ingest"


def test_adds_bearer_token_header_when_set(monkeypatch):
    monkeypatch.setenv("GRADES_INGEST_URL", "http://example.test/ingest")
    monkeypatch.setenv("GRADES_INGEST_TOKEN", "tok-123")

    captured = {}

    class _Resp:
        def __enter__(self):
            return self

        def __exit__(self, *a):
            return False

        def read(self):
            return b"{}"

    def fake_urlopen(req, timeout=10):
        captured["headers"] = dict(req.header_items())
        return _Resp()

    monkeypatch.setattr("urllib.request.urlopen", fake_urlopen)
    post_grade_to_ingest({"headline": 1})
    assert captured["headers"].get("Authorization") == "Bearer tok-123"


def test_retries_once_on_http_502_then_succeeds(monkeypatch):
    monkeypatch.setenv("GRADES_INGEST_URL", "http://example.test/ingest")
    monkeypatch.delenv("GRADES_INGEST_TOKEN", raising=False)

    calls = {"n": 0}

    class _Resp:
        def __enter__(self):
            return self

        def __exit__(self, *a):
            return False

        def read(self):
            return b"retried-ok"

    def fake_urlopen(req, timeout=10):
        calls["n"] += 1
        if calls["n"] == 1:
            raise urllib.error.HTTPError(req.full_url, 502, "Bad Gateway", hdrs=None, fp=None)
        return _Resp()

    monkeypatch.setattr("urllib.request.urlopen", fake_urlopen)
    result = post_grade_to_ingest({"headline": 1})
    assert result == "retried-ok"
    assert calls["n"] == 2


def test_raises_immediately_on_non_502_http_error(monkeypatch):
    monkeypatch.setenv("GRADES_INGEST_URL", "http://example.test/ingest")

    calls = {"n": 0}

    def fake_urlopen(req, timeout=10):
        calls["n"] += 1
        import io

        raise urllib.error.HTTPError(
            req.full_url, 400, "Bad Request", hdrs=None, fp=io.BytesIO(b"bad body")
        )

    monkeypatch.setattr("urllib.request.urlopen", fake_urlopen)
    with pytest.raises(urllib.error.HTTPError):
        post_grade_to_ingest({"headline": 1})
    assert calls["n"] == 1


def test_raises_after_second_502_failure(monkeypatch):
    monkeypatch.setenv("GRADES_INGEST_URL", "http://example.test/ingest")

    calls = {"n": 0}

    def fake_urlopen(req, timeout=10):
        calls["n"] += 1
        import io

        raise urllib.error.HTTPError(
            req.full_url, 502, "Bad Gateway", hdrs=None, fp=io.BytesIO(b"still down")
        )

    monkeypatch.setattr("urllib.request.urlopen", fake_urlopen)
    with pytest.raises(urllib.error.HTTPError):
        post_grade_to_ingest({"headline": 1})
    assert calls["n"] == 2
