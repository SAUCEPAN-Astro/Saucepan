"""Cloudflare R2 landing backend (S3-compatible via MinIO client).

Env:
  R2_ACCOUNT_ID          — Cloudflare account id (builds default endpoint)
  R2_ENDPOINT            — override API host (no scheme), e.g. <acct>.r2.cloudflarestorage.com
  R2_ACCESS_KEY_ID
  R2_SECRET_ACCESS_KEY
  R2_BUCKET              — default saucepan
  R2_USE_SSL             — default true
  R2_PUBLIC_ENDPOINT     — host clients PUT to (custom domain); defaults to R2_ENDPOINT

Set STORAGE_BACKEND=r2
"""

from __future__ import annotations

import io
import os
from datetime import timedelta
from functools import lru_cache
from pathlib import Path
from typing import Any, BinaryIO

from minio import Minio
from minio.error import S3Error

from storage.backend import StorageBackend


def _r2_endpoint() -> str:
    explicit = os.environ.get("R2_ENDPOINT", "").strip()
    if explicit:
        return explicit.replace("https://", "").replace("http://", "")
    acct = os.environ.get("R2_ACCOUNT_ID", "").strip()
    if not acct:
        raise RuntimeError("R2_ENDPOINT or R2_ACCOUNT_ID required for STORAGE_BACKEND=r2")
    return f"{acct}.r2.cloudflarestorage.com"


class R2Backend(StorageBackend):
    """Presign + CRUD against Cloudflare R2."""

    name = "r2"
    supports_client_download = True

    def __init__(self) -> None:
        self._endpoint = _r2_endpoint()
        self._access_key = os.environ.get("R2_ACCESS_KEY_ID", "")
        self._secret_key = os.environ.get("R2_SECRET_ACCESS_KEY", "")
        if not self._access_key or not self._secret_key:
            raise RuntimeError("R2_ACCESS_KEY_ID and R2_SECRET_ACCESS_KEY required")
        use_ssl = os.environ.get("R2_USE_SSL", "true").lower() in ("1", "true", "yes")
        self._use_ssl = use_ssl
        self._default_bucket = os.environ.get("R2_BUCKET", "saucepan")
        public = os.environ.get("R2_PUBLIC_ENDPOINT", "").strip()
        self._public_endpoint = (
            public.replace("https://", "").replace("http://", "") if public else self._endpoint
        )
        self._client: Minio | None = None
        self._presign_client: Minio | None = None

    @property
    def client(self) -> Minio:
        if self._client is None:
            self._client = Minio(
                self._endpoint,
                access_key=self._access_key,
                secret_key=self._secret_key,
                secure=self._use_ssl,
                region="auto",
            )
        return self._client

    @property
    def presign_client(self) -> Minio:
        """Signer whose host matches what the pier can reach."""
        if self._presign_client is None:
            self._presign_client = Minio(
                self._public_endpoint,
                access_key=self._access_key,
                secret_key=self._secret_key,
                secure=self._use_ssl,
                region="auto",
            )
        return self._presign_client

    def bucket_for_tier(self, tier: str = "default") -> str:
        del tier
        return self._default_bucket

    def presign_upload(
        self,
        bucket: str,
        object_key: str,
        *,
        expires_seconds: int = 3600,
        content_type: str = "application/fits",
    ) -> str:
        del content_type
        return self.presign_client.presigned_put_object(
            bucket,
            object_key,
            expires=timedelta(seconds=expires_seconds),
        )

    def presign_download(
        self,
        bucket: str,
        object_key: str,
        *,
        expires_seconds: int = 3600,
    ) -> str:
        return self.presign_client.presigned_get_object(
            bucket,
            object_key,
            expires=timedelta(seconds=expires_seconds),
        )

    def head_object(self, bucket: str, object_key: str) -> dict[str, Any]:
        try:
            info = self.client.stat_object(bucket, object_key)
        except S3Error as exc:
            if exc.code in {"NoSuchKey", "NoSuchObject"}:
                raise FileNotFoundError("Object not found") from exc
            raise
        return self._normalize_head(info)

    def get_object_stream(self, bucket: str, object_key: str) -> BinaryIO:
        return self.client.get_object(bucket, object_key)

    def download_object(self, bucket: str, object_key: str, dest_path: Path) -> None:
        dest_path.parent.mkdir(parents=True, exist_ok=True)
        try:
            self.client.fget_object(bucket, object_key, str(dest_path))
        except S3Error as exc:
            if exc.code in {"NoSuchKey", "NoSuchObject"}:
                raise FileNotFoundError("Object not found") from exc
            raise

    def put_object(
        self,
        bucket: str,
        object_key: str,
        data: BinaryIO,
        *,
        content_type: str | None = None,
        length: int | None = None,
    ) -> None:
        payload = data.read()
        size = length if length is not None else len(payload)
        self.client.put_object(
            bucket,
            object_key,
            io.BytesIO(payload),
            length=size,
            content_type=content_type or "application/octet-stream",
        )

    def delete_object(self, bucket: str, object_key: str) -> None:
        self.client.remove_object(bucket, object_key)


@lru_cache(maxsize=1)
def get_r2_backend() -> R2Backend:
    return R2Backend()
