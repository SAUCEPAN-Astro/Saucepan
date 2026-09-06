"""Storage backend factory — select implementation via ``STORAGE_BACKEND`` env."""

from __future__ import annotations

import os
from functools import lru_cache

from storage.backend import StorageBackend
from storage.filesystem_backend import FilesystemBackend


def _backend_name() -> str:
    # Product landing is R2 on the task apiserver. Datalake local helpers use filesystem.
    return os.environ.get("STORAGE_BACKEND", "filesystem").strip().lower()


@lru_cache(maxsize=1)
def get_storage_backend() -> StorageBackend:
    """Return the configured object storage backend (default: filesystem)."""
    name = _backend_name()
    if name in ("filesystem", "local", "fs"):
        return FilesystemBackend()
    if name == "r2":
        from storage.r2_backend import R2Backend

        return R2Backend()
    if name == "minio":
        raise ValueError("STORAGE_BACKEND=minio was removed (#394) — use filesystem or r2")
    raise ValueError(f"Unknown STORAGE_BACKEND={name!r} — expected filesystem or r2")


def reset_storage_backend() -> None:
    """Clear cached backend (tests / config reload)."""
    get_storage_backend.cache_clear()
