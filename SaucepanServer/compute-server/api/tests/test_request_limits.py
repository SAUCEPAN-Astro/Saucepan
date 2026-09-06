"""Tests for compute HTTP request resource limits (issue #256)."""

from __future__ import annotations

import threading
from unittest.mock import MagicMock

import pytest
from api.limits import DeferredWorkPool, reset_deferred_photometry_pool_for_tests


@pytest.fixture()
def storage_root(tmp_path):
    return tmp_path


@pytest.fixture()
def client(storage_root, monkeypatch):
    monkeypatch.setenv("STORAGE_ROOT", str(storage_root))
    monkeypatch.setenv("COMPUTE_ALLOW_INSECURE", "1")
    monkeypatch.setenv("MAX_STACK_FRAMES", "4")
    monkeypatch.setenv("MAX_CONTENT_LENGTH", "1024")
    monkeypatch.setenv("MAX_DEFERRED_PHOTOMETRY_WORKERS", "1")
    reset_deferred_photometry_pool_for_tests()

    from api.app import create_app

    app = create_app()
    app.config["TESTING"] = True
    yield app.test_client()
    reset_deferred_photometry_pool_for_tests()


def test_stack_rejects_too_many_frames(client):
    resp = client.post(
        "/v1/stack",
        json={
            "frame_paths": [f"frame-{i}.fits" for i in range(5)],
            "output_path": "stacked.fits",
        },
    )
    assert resp.status_code == 400
    assert "MAX_STACK_FRAMES" in resp.get_json()["error"]


def test_request_body_too_large(client):
    from api.app import create_app

    app = create_app()
    assert app.config["MAX_CONTENT_LENGTH"] == 1024

    payload = '{"staged_path":"' + ("x" * 2000) + '"}'
    resp = client.post(
        "/v1/grade",
        data=payload,
        content_type="application/json",
        content_length=len(payload),
    )
    assert resp.status_code == 413


def test_deferred_photometry_returns_503_when_pool_full(client, monkeypatch, storage_root):
    fits = storage_root / "frame.fits"
    fits.write_bytes(b"stub")

    pool_mock = MagicMock()
    pool_mock.try_submit.return_value = False
    monkeypatch.setattr("api.limits.deferred_photometry_pool", lambda: pool_mock)

    resp = client.post(
        "/v1/photometry",
        json={"staged_path": "frame.fits", "defer": True},
    )
    assert resp.status_code == 503
    assert "capacity exceeded" in resp.get_json()["error"]


def test_deferred_work_pool_rejects_when_at_capacity():
    pool = DeferredWorkPool(max_workers=1)
    gate = threading.Event()

    def block() -> None:
        gate.wait(timeout=5)

    try:
        assert pool.try_submit(block) is True
        assert pool.try_submit(lambda: None) is False
    finally:
        gate.set()
        pool.shutdown()


def test_deferred_work_pool_accepts_after_slot_freed():
    pool = DeferredWorkPool(max_workers=1)
    done = threading.Event()

    def quick() -> None:
        done.set()

    try:
        assert pool.try_submit(quick) is True
        assert done.wait(timeout=5)
        assert pool.try_submit(quick) is True
    finally:
        pool.shutdown()
