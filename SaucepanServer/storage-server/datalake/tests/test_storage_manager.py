"""Unit tests for storage/storage_manager.py — LocalStorageClient (pure filesystem)."""

from __future__ import annotations

import hashlib
import time
from pathlib import Path

import pytest

from storage.storage_manager import LocalStorageClient


@pytest.fixture()
def client(tmp_path):
    return LocalStorageClient(storage_root=str(tmp_path))


def test_init_creates_raw_and_processed_dirs(tmp_path):
    LocalStorageClient(storage_root=str(tmp_path))
    assert (tmp_path / "raw").is_dir()
    assert (tmp_path / "processed").is_dir()


def test_upload_to_staging_missing_source(client):
    result = client.upload_to_staging("/nonexistent/file.fits", "camp1")
    assert result["success"] is False
    assert "not found" in result["error"]


def test_upload_to_staging_success(client, tmp_path):
    src = tmp_path / "source.fits"
    src.write_bytes(b"fits-bytes")
    result = client.upload_to_staging(str(src), "camp1")
    assert result["success"] is True
    assert result["file_size"] == len(b"fits-bytes")
    assert result["original_filename"] == "source.fits"
    assert Path(result["staging_path"]).is_file()
    assert client.raw_root.joinpath("uploads", "camp1").is_dir()

    expected_checksum = hashlib.sha256(b"fits-bytes").hexdigest()
    assert result["checksum"] == expected_checksum


def test_upload_to_staging_rejects_campaign_path_traversal(client, tmp_path):
    src = tmp_path / "source.fits"
    src.write_bytes(b"fits-bytes")
    with pytest.raises(ValueError, match="invalid campaign_id"):
        client.upload_to_staging(str(src), "../../outside")
    assert not (tmp_path / "outside").exists()


def test_move_to_campaign_missing_source(client):
    result = client.move_to_campaign("/nonexistent/staged.fits", "camp1", "dataset1")
    assert result["success"] is False
    assert "not found" in result["error"]


def test_move_to_campaign_success(client, tmp_path):
    staged = tmp_path / "staged.fits"
    staged.write_bytes(b"staged-bytes")
    result = client.move_to_campaign(str(staged), "camp1", "dataset1")
    assert result["success"] is True
    assert result["dataset_name"] == "dataset1"
    assert not staged.exists()  # moved, not copied
    final_path = client.processed_root / "campaigns" / "camp1" / "dataset1.fits"
    assert final_path.is_file()
    assert result["checksum"] == hashlib.sha256(b"staged-bytes").hexdigest()


def test_calculate_checksum_known_value(client, tmp_path):
    f = tmp_path / "known.txt"
    f.write_bytes(b"abc")
    checksum = client.calculate_checksum(str(f))
    assert checksum == hashlib.sha256(b"abc").hexdigest()


def test_calculate_checksum_missing_file_returns_none(client):
    assert client.calculate_checksum("/does/not/exist") is None


def test_calculate_checksum_md5_algorithm(client, tmp_path):
    f = tmp_path / "known.txt"
    f.write_bytes(b"abc")
    checksum = client.calculate_checksum(str(f), algorithm="md5")
    assert checksum == hashlib.md5(b"abc").hexdigest()


def test_get_file_info_success(client, tmp_path):
    f = tmp_path / "info.fits"
    f.write_bytes(b"data-bytes")
    info = client.get_file_info(str(f))
    assert info["success"] is True
    assert info["size"] == len(b"data-bytes")
    assert info["checksum"] == hashlib.sha256(b"data-bytes").hexdigest()


def test_get_file_info_missing_file(client):
    info = client.get_file_info("/does/not/exist")
    assert info["success"] is False
    assert "not found" in info["error"]


def test_cleanup_old_files_removes_old_but_not_new(client):
    cache_dir = client.storage_root / "cache" / "temp"
    cache_dir.mkdir(parents=True)
    old_file = cache_dir / "old.tmp"
    new_file = cache_dir / "new.tmp"
    old_file.write_text("old")
    new_file.write_text("new")

    # Backdate the old file's mtime by 60 days.
    old_time = time.time() - 60 * 86400
    import os

    os.utime(old_file, (old_time, old_time))

    result = client.cleanup_old_files(days_old=30)
    assert result["success"] is True
    assert result["files_cleaned"] == 1
    assert not old_file.exists()
    assert new_file.exists()


def test_cleanup_old_files_no_cache_dir_is_noop(client):
    result = client.cleanup_old_files()
    assert result == {"success": True, "files_cleaned": 0}
