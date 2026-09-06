"""HTTP request resource limits for compute API (DoS guards)."""

from __future__ import annotations

import os
import threading
from collections.abc import Callable
from concurrent.futures import ThreadPoolExecutor

DEFAULT_MAX_CONTENT_LENGTH = 256 * 1024  # 256 KiB JSON bodies
DEFAULT_MAX_DEFERRED_PHOTOMETRY_WORKERS = 4

# Memory-aware /v1/stack frame cap (#414). The stacking combiner walks the
# frame in STACK_TILE_PX tiles and accumulates a tile at a time, so the
# ceiling is a function of the accumulator budget, not a magic 64. Kept in
# sync with saucepan_pipeline.stacking.config (that module is the authority
# for the combiner itself; this mirror avoids importing the pipeline
# package into the HTTP layer).
DEFAULT_STACK_TILE_PX = 1024
DEFAULT_STACK_MEM_BUDGET_MB = 4096
_STACK_MIN_FRAMES = 8
_STACK_HARD_MAX_FRAMES = 512
_STACK_TILE_BYTES_PER_FRAME = 16  # 2 * sizeof(float64)


def _env_int(name: str, default: int) -> int:
    raw = os.environ.get(name, "").strip()
    if not raw:
        return default
    try:
        value = int(raw)
    except ValueError:
        return default
    return value if value > 0 else default


def max_stack_frames() -> int:
    """Frames-per-stack cap for ``POST /v1/stack`` (#414).

    ``STACK_MEM_BUDGET_MB / (STACK_TILE_PX**2 * 16)``, clamped to
    ``[8, 512]`` - the stacking combiner tiles its accumulator, so the
    ceiling tracks the accumulator budget rather than a fixed 64. A
    positive ``MAX_STACK_FRAMES`` overrides the computation entirely (only
    the hard 512 ceiling still applies).
    """
    override = _env_int("MAX_STACK_FRAMES", 0)
    if override > 0:
        return min(_STACK_HARD_MAX_FRAMES, override)

    tile_px = _env_int("STACK_TILE_PX", DEFAULT_STACK_TILE_PX)
    budget_mb = _env_int("STACK_MEM_BUDGET_MB", DEFAULT_STACK_MEM_BUDGET_MB)
    computed = int(
        (budget_mb * 1024 * 1024)
        / (tile_px * tile_px * _STACK_TILE_BYTES_PER_FRAME)
    )
    return max(_STACK_MIN_FRAMES, min(_STACK_HARD_MAX_FRAMES, computed))


def max_content_length() -> int:
    return _env_int("MAX_CONTENT_LENGTH", DEFAULT_MAX_CONTENT_LENGTH)


def max_deferred_photometry_workers() -> int:
    return _env_int(
        "MAX_DEFERRED_PHOTOMETRY_WORKERS",
        DEFAULT_MAX_DEFERRED_PHOTOMETRY_WORKERS,
    )


class DeferredWorkPool:
    """Bounded thread pool for fire-and-forget deferred photometry."""

    def __init__(self, max_workers: int | None = None) -> None:
        workers = max_workers or max_deferred_photometry_workers()
        self._max_workers = workers
        self._executor = ThreadPoolExecutor(
            max_workers=workers,
            thread_name_prefix="defer-photo",
        )
        self._semaphore = threading.Semaphore(workers)

    def try_submit(self, fn: Callable[[], None]) -> bool:
        if not self._semaphore.acquire(blocking=False):
            return False

        def _wrapped() -> None:
            try:
                fn()
            finally:
                self._semaphore.release()

        self._executor.submit(_wrapped)
        return True

    def shutdown(self) -> None:
        self._executor.shutdown(wait=False, cancel_futures=True)


_pool: DeferredWorkPool | None = None
_pool_lock = threading.Lock()


def deferred_photometry_pool() -> DeferredWorkPool:
    global _pool
    if _pool is None:
        with _pool_lock:
            if _pool is None:
                _pool = DeferredWorkPool()
    return _pool


def reset_deferred_photometry_pool_for_tests() -> None:
    """Reset module pool singleton (tests only)."""
    global _pool
    with _pool_lock:
        if _pool is not None:
            _pool.shutdown()
        _pool = None
