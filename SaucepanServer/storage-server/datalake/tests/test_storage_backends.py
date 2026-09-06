"""Unit tests for the filesystem backend."""

from __future__ import annotations

import io

import pytest

from storage.filesystem_backend import FilesystemBackend


@pytest.fixture()
def fs_backend(tmp_path):
    return FilesystemBackend(storage_root=str(tmp_path))


def test_bucket_for_tier_default(fs_backend, monkeypatch):
    monkeypatch.delenv("STORAGE_BUCKET", raising=False)
    assert fs_backend.bucket_for_tier() == "saucepan"


def test_bucket_for_tier_env_override(tmp_path, monkeypatch):
    monkeypatch.setenv("STORAGE_BUCKET", "custom")
    backend = FilesystemBackend(storage_root=str(tmp_path))
    assert backend.bucket_for_tier("hot") == "custom"


def test_presign_upload_and_download_return_file_uri(fs_backend):
    url = fs_backend.presign_upload("bucket", "campaign/frame.fits")
    assert url.startswith("file://")
    assert "campaign/frame.fits" in url

    url2 = fs_backend.presign_download("bucket", "campaign/frame.fits")
    assert url2.startswith("file://")


def test_put_object_then_head_and_get_stream_roundtrip(fs_backend):
    data = io.BytesIO(b"hello fits bytes")
    fs_backend.put_object("bucket", "c/frame.fits", data)

    info = fs_backend.head_object("bucket", "c/frame.fits")
    assert info["size"] == len(b"hello fits bytes")
    assert info["content_type"] is None
    assert info["etag"] is None
    assert info["last_modified"] is not None

    with fs_backend.get_object_stream("bucket", "c/frame.fits") as stream:
        assert stream.read() == b"hello fits bytes"


def test_head_object_missing_raises(fs_backend):
    with pytest.raises(FileNotFoundError):
        fs_backend.head_object("bucket", "missing/nope.fits")


def test_get_object_stream_missing_raises(fs_backend):
    with pytest.raises(FileNotFoundError):
        fs_backend.get_object_stream("bucket", "missing/nope.fits")


def test_delete_object_removes_file(fs_backend):
    fs_backend.put_object("bucket", "c/frame.fits", io.BytesIO(b"data"))
    path = fs_backend._object_path("bucket", "c/frame.fits")
    assert path.is_file()
    fs_backend.delete_object("bucket", "c/frame.fits")
    assert not path.is_file()


def test_delete_object_missing_is_noop(fs_backend):
    fs_backend.delete_object("bucket", "does/not/exist.fits")  # must not raise


def test_object_key_strips_leading_slash(fs_backend):
    path = fs_backend._object_path("bucket", "/leading/slash.fits")
    assert "objects/bucket/leading/slash.fits" in str(path)


def test_object_key_cannot_escape_storage_root(fs_backend, tmp_path):
    with pytest.raises(ValueError, match="path escapes storage root"):
        fs_backend.put_object("bucket", "../../outside.fits", io.BytesIO(b"secret"))
    assert not (tmp_path / "outside.fits").exists()


def test_download_object_default_impl_writes_dest(tmp_path, fs_backend):
    fs_backend.put_object("bucket", "c/frame.fits", io.BytesIO(b"payload"))
    dest = tmp_path / "downloaded" / "out.fits"
    fs_backend.download_object("bucket", "c/frame.fits", dest)
    assert dest.read_bytes() == b"payload"
