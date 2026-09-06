"""Edge cases for api.limits env parsing and the deferred-work pool."""

from __future__ import annotations

from api.limits import (
    deferred_photometry_pool,
    max_content_length,
    max_deferred_photometry_workers,
    max_stack_frames,
    reset_deferred_photometry_pool_for_tests,
)


# 256 = the memory-aware default (#414): STACK_MEM_BUDGET_MB=4096 over a
# 1024 px tile at 16 bytes/frame/pixel -> 4096 / 16.
_COMPUTED_DEFAULT = 256


def test_max_stack_frames_default_when_unset(monkeypatch):
    monkeypatch.delenv("MAX_STACK_FRAMES", raising=False)
    monkeypatch.delenv("STACK_TILE_PX", raising=False)
    monkeypatch.delenv("STACK_MEM_BUDGET_MB", raising=False)
    assert max_stack_frames() == _COMPUTED_DEFAULT


def test_max_stack_frames_invalid_value_falls_back_to_default(monkeypatch):
    monkeypatch.setenv("MAX_STACK_FRAMES", "not-an-int")
    assert max_stack_frames() == _COMPUTED_DEFAULT


def test_max_stack_frames_zero_or_negative_falls_back_to_default(monkeypatch):
    monkeypatch.setenv("MAX_STACK_FRAMES", "0")
    assert max_stack_frames() == _COMPUTED_DEFAULT
    monkeypatch.setenv("MAX_STACK_FRAMES", "-5")
    assert max_stack_frames() == _COMPUTED_DEFAULT


def test_max_stack_frames_valid_override(monkeypatch):
    monkeypatch.setenv("MAX_STACK_FRAMES", "10")
    assert max_stack_frames() == 10


def test_max_content_length_blank_env_uses_default(monkeypatch):
    monkeypatch.setenv("MAX_CONTENT_LENGTH", "   ")
    assert max_content_length() == 256 * 1024


def test_max_deferred_photometry_workers_invalid_falls_back(monkeypatch):
    monkeypatch.setenv("MAX_DEFERRED_PHOTOMETRY_WORKERS", "abc")
    assert max_deferred_photometry_workers() == 4


def test_deferred_photometry_pool_singleton_reused(monkeypatch):
    reset_deferred_photometry_pool_for_tests()
    try:
        pool_a = deferred_photometry_pool()
        pool_b = deferred_photometry_pool()
        assert pool_a is pool_b
    finally:
        reset_deferred_photometry_pool_for_tests()


def test_reset_pool_for_tests_creates_fresh_instance():
    reset_deferred_photometry_pool_for_tests()
    try:
        pool_a = deferred_photometry_pool()
        reset_deferred_photometry_pool_for_tests()
        pool_b = deferred_photometry_pool()
        assert pool_a is not pool_b
    finally:
        reset_deferred_photometry_pool_for_tests()


def test_reset_pool_when_never_created_is_a_no_op():
    reset_deferred_photometry_pool_for_tests()
    reset_deferred_photometry_pool_for_tests()  # second call: _pool already None
