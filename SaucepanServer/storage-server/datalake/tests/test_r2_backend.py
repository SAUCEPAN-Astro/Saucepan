"""Unit tests for R2Backend — the Cloudflare R2 wrapper around minio.Minio.

Never hits real network: minio.Minio is patched out entirely.
"""

from __future__ import annotations

import io
from unittest.mock import MagicMock, patch

import pytest


def _set_r2_env(monkeypatch, **extra):
    monkeypatch.setenv("R2_ACCOUNT_ID", "acct123")
    monkeypatch.setenv("R2_ACCESS_KEY_ID", "key")
    monkeypatch.setenv("R2_SECRET_ACCESS_KEY", "secret")
    monkeypatch.delenv("R2_ENDPOINT", raising=False)
    monkeypatch.delenv("R2_PUBLIC_ENDPOINT", raising=False)
    monkeypatch.delenv("R2_BUCKET", raising=False)
    monkeypatch.delenv("R2_USE_SSL", raising=False)
    for k, v in extra.items():
        monkeypatch.setenv(k, v)


def test_endpoint_built_from_account_id(monkeypatch):
    from storage.r2_backend import R2Backend

    _set_r2_env(monkeypatch)
    backend = R2Backend()
    assert backend._endpoint == "acct123.r2.cloudflarestorage.com"


def test_endpoint_explicit_override_strips_scheme(monkeypatch):
    from storage.r2_backend import R2Backend

    _set_r2_env(monkeypatch, R2_ENDPOINT="https://custom.example.com")
    backend = R2Backend()
    assert backend._endpoint == "custom.example.com"


def test_missing_account_and_endpoint_raises(monkeypatch):
    from storage.r2_backend import R2Backend

    monkeypatch.delenv("R2_ENDPOINT", raising=False)
    monkeypatch.delenv("R2_ACCOUNT_ID", raising=False)
    monkeypatch.setenv("R2_ACCESS_KEY_ID", "key")
    monkeypatch.setenv("R2_SECRET_ACCESS_KEY", "secret")
    with pytest.raises(RuntimeError, match="R2_ENDPOINT or R2_ACCOUNT_ID"):
        R2Backend()


def test_missing_credentials_raises(monkeypatch):
    from storage.r2_backend import R2Backend

    monkeypatch.setenv("R2_ACCOUNT_ID", "acct123")
    monkeypatch.delenv("R2_ACCESS_KEY_ID", raising=False)
    monkeypatch.delenv("R2_SECRET_ACCESS_KEY", raising=False)
    with pytest.raises(RuntimeError, match="R2_ACCESS_KEY_ID"):
        R2Backend()


def test_bucket_for_tier_returns_default(monkeypatch):
    from storage.r2_backend import R2Backend

    _set_r2_env(monkeypatch, R2_BUCKET="mybucket")
    backend = R2Backend()
    assert backend.bucket_for_tier("anything") == "mybucket"


def test_public_endpoint_defaults_to_endpoint(monkeypatch):
    from storage.r2_backend import R2Backend

    _set_r2_env(monkeypatch)
    backend = R2Backend()
    assert backend._public_endpoint == backend._endpoint


def test_public_endpoint_override(monkeypatch):
    from storage.r2_backend import R2Backend

    _set_r2_env(monkeypatch, R2_PUBLIC_ENDPOINT="https://pub.example.com")
    backend = R2Backend()
    assert backend._public_endpoint == "pub.example.com"
    assert backend._public_endpoint != backend._endpoint


@patch("storage.r2_backend.Minio")
def test_client_is_lazily_constructed_and_cached(mock_minio, monkeypatch):
    from storage.r2_backend import R2Backend

    _set_r2_env(monkeypatch)
    backend = R2Backend()
    instance = MagicMock()
    mock_minio.return_value = instance

    c1 = backend.client
    c2 = backend.client
    assert c1 is c2
    mock_minio.assert_called_once()
    kwargs = mock_minio.call_args.kwargs
    assert kwargs["access_key"] == "key"
    assert kwargs["secret_key"] == "secret"
    assert kwargs["region"] == "auto"


@patch("storage.r2_backend.Minio")
def test_presign_client_uses_public_endpoint(mock_minio, monkeypatch):
    from storage.r2_backend import R2Backend

    _set_r2_env(monkeypatch, R2_PUBLIC_ENDPOINT="https://pub.example.com")
    backend = R2Backend()
    _ = backend.presign_client
    args, kwargs = mock_minio.call_args
    assert args[0] == "pub.example.com"


@patch("storage.r2_backend.Minio")
def test_presign_upload_calls_presigned_put_object(mock_minio, monkeypatch):
    from storage.r2_backend import R2Backend

    _set_r2_env(monkeypatch)
    instance = MagicMock()
    instance.presigned_put_object.return_value = "https://r2.test/put"
    mock_minio.return_value = instance

    backend = R2Backend()
    url = backend.presign_upload("bucket", "key.fits", expires_seconds=60)
    assert url == "https://r2.test/put"
    instance.presigned_put_object.assert_called_once()
    args, kwargs = instance.presigned_put_object.call_args
    assert args[0] == "bucket"
    assert args[1] == "key.fits"


@patch("storage.r2_backend.Minio")
def test_presign_download_calls_presigned_get_object(mock_minio, monkeypatch):
    from storage.r2_backend import R2Backend

    _set_r2_env(monkeypatch)
    instance = MagicMock()
    instance.presigned_get_object.return_value = "https://r2.test/get"
    mock_minio.return_value = instance

    backend = R2Backend()
    url = backend.presign_download("bucket", "key.fits")
    assert url == "https://r2.test/get"
    instance.presigned_get_object.assert_called_once()


@patch("storage.r2_backend.Minio")
def test_head_object_normalizes_stat_response(mock_minio, monkeypatch):
    from storage.r2_backend import R2Backend

    _set_r2_env(monkeypatch)
    instance = MagicMock()
    stat = MagicMock(size=42, etag="abc", content_type="application/fits", last_modified="now")
    instance.stat_object.return_value = stat
    mock_minio.return_value = instance

    backend = R2Backend()
    info = backend.head_object("bucket", "key.fits")
    assert info == {
        "size": 42,
        "etag": "abc",
        "content_type": "application/fits",
        "last_modified": "now",
    }


@patch("storage.r2_backend.Minio")
def test_get_object_stream_returns_unconsumed_response_stream(mock_minio, monkeypatch):
    from storage.r2_backend import R2Backend

    _set_r2_env(monkeypatch)
    instance = MagicMock()
    response = MagicMock()
    response.read.return_value = b"chunk"
    instance.get_object.return_value = response
    mock_minio.return_value = instance

    backend = R2Backend()
    stream = backend.get_object_stream("bucket", "key.fits")
    assert stream is response
    response.read.assert_not_called()

    assert stream.read(5) == b"chunk"
    response.read.assert_called_once_with(5)


@patch("storage.r2_backend.Minio")
def test_download_object_calls_fget_object_and_makes_parent_dir(mock_minio, monkeypatch, tmp_path):
    from storage.r2_backend import R2Backend

    _set_r2_env(monkeypatch)
    instance = MagicMock()
    mock_minio.return_value = instance

    backend = R2Backend()
    dest = tmp_path / "nested" / "out.fits"
    backend.download_object("bucket", "key.fits", dest)
    assert dest.parent.is_dir()
    instance.fget_object.assert_called_once_with("bucket", "key.fits", str(dest))


@patch("storage.r2_backend.Minio")
def test_put_object_computes_length_when_not_given(mock_minio, monkeypatch):
    from storage.r2_backend import R2Backend

    _set_r2_env(monkeypatch)
    instance = MagicMock()
    mock_minio.return_value = instance

    backend = R2Backend()
    backend.put_object("bucket", "key.fits", io.BytesIO(b"12345"))
    args, kwargs = instance.put_object.call_args
    assert kwargs["length"] == 5
    assert kwargs["content_type"] == "application/octet-stream"


@patch("storage.r2_backend.Minio")
def test_delete_object_calls_remove_object(mock_minio, monkeypatch):
    from storage.r2_backend import R2Backend

    _set_r2_env(monkeypatch)
    instance = MagicMock()
    mock_minio.return_value = instance

    backend = R2Backend()
    backend.delete_object("bucket", "key.fits")
    instance.remove_object.assert_called_once_with("bucket", "key.fits")


@patch("storage.r2_backend.Minio")
def test_get_r2_backend_is_cached(mock_minio, monkeypatch):
    from storage.r2_backend import get_r2_backend

    _set_r2_env(monkeypatch)
    get_r2_backend.cache_clear()
    b1 = get_r2_backend()
    b2 = get_r2_backend()
    assert b1 is b2
    get_r2_backend.cache_clear()
