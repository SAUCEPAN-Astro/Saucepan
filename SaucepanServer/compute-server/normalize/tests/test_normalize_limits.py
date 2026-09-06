"""Normalize FITS size guards — reject oversize headers before pixel access."""

from __future__ import annotations

import sys
from pathlib import Path
from unittest import mock

import numpy as np
import pytest
from astropy.io import fits

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))

from normalize.normalize import normalize_fits


def _write_fits(path: Path, *, naxis1: int, naxis2: int) -> None:
    data = np.zeros((naxis2, naxis1), dtype=np.float32)
    hdu = fits.PrimaryHDU(data=data)
    hdu.header["NAXIS1"] = naxis1
    hdu.header["NAXIS2"] = naxis2
    hdu.writeto(path, overwrite=True)


def test_normalize_rejects_oversize_naxis_before_checksum(tmp_path, monkeypatch) -> None:
    monkeypatch.setenv("MAX_FITS_PIXELS", "1000")
    inp = tmp_path / "bomb.fits"
    out = tmp_path / "out.fits"
    _write_fits(inp, naxis1=5000, naxis2=5000)

    with mock.patch("grading.fits_limits.checksum_primary_data") as checksum_mock:
        result = normalize_fits(str(inp), str(out))

    checksum_mock.assert_not_called()
    assert result.success is False
    assert "pixel count" in (result.error or "").lower()
    assert not out.exists()


def test_normalize_checksum_uses_streaming_helper(tmp_path, monkeypatch) -> None:
    monkeypatch.setenv("MAX_FITS_PIXELS", "1_000_000")
    monkeypatch.setenv("MAX_FITS_BYTES", str(10 * 1024 * 1024))
    inp = tmp_path / "ok.fits"
    out = tmp_path / "out.fits"
    _write_fits(inp, naxis1=8, naxis2=8)

    import grading.fits_limits as fl

    with mock.patch(
        "grading.fits_limits.checksum_primary_data",
        wraps=fl.checksum_primary_data,
    ) as checksum_mock:
        result = normalize_fits(str(inp), str(out))

    checksum_mock.assert_called_once()
    assert result.success is True
    assert out.exists()
