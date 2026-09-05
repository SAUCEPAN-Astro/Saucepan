"""Tests for FITS dimension / file-size guards."""

from __future__ import annotations

import numpy as np
import pytest
from astropy.io import fits
from grading.fits_limits import (
    FitsSizeLimitError,
    checksum_primary_data,
    ensure_fits_loadable,
    pixel_count_from_header,
)


def _write_small_fits(path, *, naxis1: int = 10, naxis2: int = 10) -> None:
    data = np.zeros((naxis2, naxis1), dtype=np.float32)
    hdu = fits.PrimaryHDU(data=data)
    hdu.header["NAXIS1"] = naxis1
    hdu.header["NAXIS2"] = naxis2
    hdu.writeto(path, overwrite=True)


def test_pixel_count_from_header_product() -> None:
    hdr = fits.Header()
    hdr["NAXIS"] = 2
    hdr["NAXIS1"] = 2048
    hdr["NAXIS2"] = 2048
    assert pixel_count_from_header(hdr) == 2048 * 2048


def test_rejects_oversize_naxis_header(tmp_path, monkeypatch) -> None:
    monkeypatch.setenv("MAX_FITS_PIXELS", "1000")
    path = tmp_path / "bomb.fits"
    _write_small_fits(path, naxis1=10_000, naxis2=10_000)

    with fits.open(path) as hdul:
        with pytest.raises(FitsSizeLimitError, match="pixel count"):
            ensure_fits_loadable(str(path), hdul[0].header)


def test_allows_small_frame(tmp_path, monkeypatch) -> None:
    monkeypatch.setenv("MAX_FITS_PIXELS", "1_000_000")
    monkeypatch.setenv("MAX_FITS_BYTES", str(10 * 1024 * 1024))
    path = tmp_path / "ok.fits"
    _write_small_fits(path)

    with fits.open(path) as hdul:
        ensure_fits_loadable(str(path), hdul[0].header)


def test_rejects_oversize_file(tmp_path, monkeypatch) -> None:
    monkeypatch.setenv("MAX_FITS_BYTES", "64")
    path = tmp_path / "big.fits"
    _write_small_fits(path)

    with fits.open(path) as hdul:
        with pytest.raises(FitsSizeLimitError, match="file size"):
            ensure_fits_loadable(str(path), hdul[0].header)


def test_checksum_primary_data_matches_full_hash() -> None:
    data = np.arange(12, dtype=np.int16).reshape(3, 4)
    import hashlib

    expected = "sha256:" + hashlib.sha256(data.tobytes()).hexdigest()
    assert checksum_primary_data(data) == expected


def test_checksum_primary_data_empty() -> None:
    assert (
        checksum_primary_data(None)
        == "sha256:" + "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
    )
