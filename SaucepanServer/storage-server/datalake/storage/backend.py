"""Object storage backend protocol used by the local R2 landing helper."""

from __future__ import annotations

from abc import ABC, abstractmethod
from pathlib import Path
from typing import Any, BinaryIO


def safe_path_component(value: str, field: str) -> str:
    """Validate a user-controlled value before using it as one path segment."""
    component = str(value).strip()
    if (
        not component
        or component in {".", ".."}
        or "/" in component
        or "\\" in component
        or "\x00" in component
    ):
        raise ValueError(f"invalid {field}")
    return component


def confined_path(root: Path, *parts: str) -> Path:
    """Resolve a path and reject traversal or symlink escapes from ``root``."""
    resolved_root = root.resolve()
    candidate = resolved_root.joinpath(*parts).resolve()
    try:
        candidate.relative_to(resolved_root)
    except ValueError as exc:
        raise ValueError("path escapes storage root") from exc
    return candidate


class StorageBackend(ABC):
    """S3-compatible object storage — presign, head, stream, CRUD."""

    name: str = "base"
    supports_client_download: bool = False

    @abstractmethod
    def bucket_for_tier(self, tier: str = "default") -> str:
        """Resolve the configured landing bucket."""

    @abstractmethod
    def presign_upload(
        self,
        bucket: str,
        object_key: str,
        *,
        expires_seconds: int = 3600,
        content_type: str = "application/fits",
    ) -> str:
        """Return a presigned PUT URL for direct client upload."""

    @abstractmethod
    def presign_download(
        self,
        bucket: str,
        object_key: str,
        *,
        expires_seconds: int = 3600,
    ) -> str:
        """Return a presigned GET URL for client download."""

    @abstractmethod
    def head_object(self, bucket: str, object_key: str) -> dict[str, Any]:
        """Return object metadata (size, etag, content_type, last_modified)."""

    @abstractmethod
    def get_object_stream(self, bucket: str, object_key: str) -> BinaryIO:
        """Open a readable stream for the object body."""

    @abstractmethod
    def put_object(
        self,
        bucket: str,
        object_key: str,
        data: BinaryIO,
        *,
        content_type: str | None = None,
        length: int | None = None,
    ) -> None:
        """Upload object bytes from ``data``."""

    @abstractmethod
    def delete_object(self, bucket: str, object_key: str) -> None:
        """Remove object from bucket."""

    def download_object(self, bucket: str, object_key: str, dest_path: Path) -> None:
        """Download object to local path (default: stream + write)."""
        dest_path.parent.mkdir(parents=True, exist_ok=True)
        with self.get_object_stream(bucket, object_key) as stream:
            dest_path.write_bytes(stream.read())

    @staticmethod
    def _normalize_head(info: Any) -> dict[str, Any]:
        """Map vendor stat response to a common dict shape."""
        return {
            "size": getattr(info, "size", None),
            "etag": getattr(info, "etag", None),
            "content_type": getattr(info, "content_type", None),
            "last_modified": getattr(info, "last_modified", None),
        }
