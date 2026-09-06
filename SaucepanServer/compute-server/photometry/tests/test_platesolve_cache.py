"""Tests for photometry plate-solve cache."""

from __future__ import annotations

import pathlib
import sys

ROOT = pathlib.Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT.parent))

from photometry import platesolve_cache


def setup_function() -> None:
    platesolve_cache.clear()


def test_make_key_requires_both_parts():
    assert platesolve_cache.make_key("u1", "abc") == ("u1", "abc")
    assert platesolve_cache.make_key(None, "abc") is None
    assert platesolve_cache.make_key("u1", None) is None


def test_put_get_roundtrip():
    key = platesolve_cache.make_key("upload-42", "sha256:deadbeef")
    result = {"ok": True, "method": "stub", "ra": 180.0, "dec": 45.0}
    platesolve_cache.put(key, result)
    hit = platesolve_cache.get(key)
    assert hit is not None
    assert hit["ok"] is True
    assert hit["ra"] == 180.0
    assert hit is not result


def test_cache_isolated_by_upload_and_checksum():
    key_a = platesolve_cache.make_key("u1", "chk-a")
    key_b = platesolve_cache.make_key("u1", "chk-b")
    platesolve_cache.put(key_a, {"ok": True, "method": "a"})
    platesolve_cache.put(key_b, {"ok": False, "method": "b"})
    assert platesolve_cache.get(key_a)["method"] == "a"
    assert platesolve_cache.get(key_b)["method"] == "b"


def test_miss_returns_none():
    key = platesolve_cache.make_key("missing", "nope")
    assert platesolve_cache.get(key) is None


def test_get_with_none_key_returns_none():
    assert platesolve_cache.get(None) is None


def test_put_with_none_key_is_a_no_op():
    # Should not raise and should not populate the cache under any key.
    platesolve_cache.put(None, {"ok": True})
    key = platesolve_cache.make_key("u1", "chk")
    assert platesolve_cache.get(key) is None


def test_get_returns_copy_not_shared_reference():
    key = platesolve_cache.make_key("upload-1", "chk-1")
    platesolve_cache.put(key, {"ok": True, "nested": {"a": 1}})
    hit1 = platesolve_cache.get(key)
    hit1["ok"] = False
    hit2 = platesolve_cache.get(key)
    assert hit2["ok"] is True
