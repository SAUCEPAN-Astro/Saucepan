"""Shared plate-solve cache keyed by upload_id + frame checksum."""

from __future__ import annotations

import threading
import typing

PlateSolveResult = dict[str, typing.Any]
CacheKey = tuple[str, str]

_lock = threading.Lock()
_cache: dict[CacheKey, PlateSolveResult] = {}


def make_key(upload_id: str | None, checksum: str | None) -> CacheKey | None:
    """Build cache key; returns None when either component is missing."""
    if not upload_id or not checksum:
        return None
    return (str(upload_id).strip(), str(checksum).strip())


def get(key: CacheKey | None) -> PlateSolveResult | None:
    """Return cached plate-solve result or None."""
    if key is None:
        return None
    with _lock:
        hit = _cache.get(key)
        return dict(hit) if hit is not None else None


def put(key: CacheKey | None, result: PlateSolveResult) -> None:
    """Store plate-solve result (fail-open, in-process only)."""
    if key is None:
        return
    with _lock:
        _cache[key] = dict(result)


def clear() -> None:
    """Clear cache (tests)."""
    with _lock:
        _cache.clear()
