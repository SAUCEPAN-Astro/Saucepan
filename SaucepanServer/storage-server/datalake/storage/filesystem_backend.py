"""Local filesystem object storage for datalake grade helpers (no MinIO)."""

from __future__ import annotations

import os
import shutil
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, BinaryIO
from urllib.parse import quote

from storage.backend import StorageBackend, confined_path, safe_path_component


class FilesystemBackend(StorageBackend):
    """Store objects under ``STORAGE_ROOT/objects/<bucket>/<key>``."""

    name = "filesystem"
    supports_client_download = False

    def __init__(self, storage_root: str | None = None) -> None:
        root = storage_root or os.environ.get("STORAGE_ROOT", "/data")
        self.root = Path(root)
        self._default_bucket = os.environ.get("STORAGE_BUCKET", "saucepan")

    def _object_path(self, bucket: str, object_key: str) -> Path:
        safe_bucket = safe_path_component(bucket, "bucket")
        safe_key = str(object_key).lstrip("/")
        if not safe_key or "\x00" in safe_key:
            raise ValueError("invalid object key")
        return confined_path(self.root / "objects", safe_bucket, safe_key)

    def bucket_for_tier(self, tier: str = "default") -> str:
        _ = tier
        return self._default_bucket

    def presign_upload(
        self,
        bucket: str,
        object_key: str,
        *,
        expires_seconds: int = 3600,
        content_type: str = "application/fits",
    ) -> str:
        _ = expires_seconds, content_type
        # Local helper only — clients should use task apiserver R2 presign.
        return f"file://{quote(str(self._object_path(bucket, object_key)))}"

    def presign_download(
        self,
        bucket: str,
        object_key: str,
        *,
        expires_seconds: int = 3600,
    ) -> str:
        _ = expires_seconds
        return f"file://{quote(str(self._object_path(bucket, object_key)))}"

    def head_object(self, bucket: str, object_key: str) -> dict[str, Any]:
        path = self._object_path(bucket, object_key)
        if not path.is_file():
            raise FileNotFoundError(f"Object not found: {bucket}/{object_key}")
        stat = path.stat()
        return {
            "size": stat.st_size,
            "etag": None,
            "content_type": None,
            "last_modified": datetime.fromtimestamp(stat.st_mtime, tz=timezone.utc),
        }

    def get_object_stream(self, bucket: str, object_key: str) -> BinaryIO:
        path = self._object_path(bucket, object_key)
        if not path.is_file():
            raise FileNotFoundError(f"Object not found: {bucket}/{object_key}")
        return path.open("rb")

    def put_object(
        self,
        bucket: str,
        object_key: str,
        data: BinaryIO,
        *,
        content_type: str | None = None,
        length: int | None = None,
    ) -> None:
        _ = content_type, length
        path = self._object_path(bucket, object_key)
        path.parent.mkdir(parents=True, exist_ok=True)
        with path.open("wb") as dest:
            shutil.copyfileobj(data, dest)

    def delete_object(self, bucket: str, object_key: str) -> None:
        path = self._object_path(bucket, object_key)
        if path.is_file():
            path.unlink()
