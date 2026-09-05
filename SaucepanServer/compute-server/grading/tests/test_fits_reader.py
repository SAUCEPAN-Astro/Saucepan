"""Tests for grading.fits_reader.read_sp_headers against real minimal FITS files."""

from __future__ import annotations

import numpy as np
import pytest
from astropy.io import fits
from grading.fits_reader import _header_float, read_sp_headers


def _write_fits(path, header: dict) -> None:
    data = np.zeros((4, 4), dtype=np.float32)
    hdu = fits.PrimaryHDU(data=data)
    for key, value in header.items():
        hdu.header[key] = value
    hdu.writeto(path, overwrite=True)


def test_reads_sp_prefixed_headers(tmp_path):
    path = tmp_path / "frame.fits"
    _write_fits(
        path,
        {
            "SP_EXPTIME": 30.0,
            "SP_FILTER": "R",
            "SP_FWHM": 2.1,
            "SP_CALSTAT": "BDF",
            "SP_SNR": 45.0,
            "SP_QUAL": 0.9,
            "SP_PIXSCALE": 1.2,
            "SP_RA": 10.5,
            "SP_DEC": -20.0,
            "SP_DATEOBS": "2026-01-01T00:00:00",
            "SP_EMULATOR": 0,
            "CTYPE1": "RA---TAN",
            "CTYPE2": "DEC--TAN",
            "CRVAL1": 10.5,
            "CRPIX1": 512.0,
            "CRVAL2": -20.0,
            "CRPIX2": 512.0,
        },
    )
    headers = read_sp_headers(str(path))
    assert headers["sp_exptime"] == 30.0
    assert headers["sp_filter"] == "R"
    assert headers["sp_fwhm"] == 2.1
    assert headers["sp_calstat"] == "BDF"
    assert headers["sp_snr"] == 45.0
    assert headers["sp_qual"] == 0.9
    assert headers["sp_pixscale"] == 1.2
    assert headers["sp_ra"] == 10.5
    assert headers["sp_dec"] == -20.0
    assert headers["sp_dateobs"] == "2026-01-01T00:00:00"
    assert headers["sp_emulator"] is False
    assert headers["ctype1"] == "RA---TAN"
    assert headers["ctype2"] == "DEC--TAN"
    assert headers["crval1"] == 10.5
    assert headers["crpix1"] == 512.0
    assert headers["crval2"] == -20.0
    assert headers["crpix2"] == 512.0


def test_falls_back_to_raw_instrument_headers_when_sp_missing(tmp_path):
    path = tmp_path / "raw.fits"
    _write_fits(
        path,
        {
            "EXPTIME": 15.0,
            "FILTER": "G",
            "SEEING": 3.0,
            "RA": 100.0,
            "DEC": 5.0,
            "DATE-OBS": "2025-06-01T12:00:00",
        },
    )
    headers = read_sp_headers(str(path))
    assert headers["sp_exptime"] == 15.0
    assert headers["sp_filter"] == "G"
    assert headers["sp_fwhm"] == 3.0
    assert headers["sp_ra"] == 100.0
    assert headers["sp_dec"] == 5.0
    assert headers["sp_dateobs"] == "2025-06-01T12:00:00"


def test_completely_empty_header_returns_all_none_or_false(tmp_path):
    path = tmp_path / "empty.fits"
    _write_fits(path, {})
    headers = read_sp_headers(str(path))
    assert headers["sp_exptime"] is None
    assert headers["sp_filter"] is None
    assert headers["sp_fwhm"] is None
    assert headers["sp_pixscale"] is None
    assert headers["sp_calstat"] is None
    assert headers["sp_snr"] is None
    assert headers["sp_qual"] is None
    assert headers["sp_ra"] is None
    assert headers["sp_dec"] is None
    assert headers["sp_dateobs"] is None
    assert headers["sp_emulator"] is False
    assert headers["ctype1"] is None
    assert headers["ctype2"] is None
    assert headers["crval1"] is None
    assert headers["crpix1"] is None
    assert headers["crval2"] is None
    assert headers["crpix2"] is None


def test_sp_emulator_true_when_set_to_one(tmp_path):
    path = tmp_path / "emu.fits"
    _write_fits(path, {"SP_EMULATOR": 1})
    headers = read_sp_headers(str(path))
    assert headers["sp_emulator"] is True


def test_sp_prefixed_takes_precedence_over_raw(tmp_path):
    path = tmp_path / "both.fits"
    _write_fits(path, {"SP_EXPTIME": 99.0, "EXPTIME": 1.0})
    headers = read_sp_headers(str(path))
    assert headers["sp_exptime"] == 99.0


def test_wcs_pixel_scale_fallback_uses_cdelt2(tmp_path):
    path = tmp_path / "wcs.fits"
    _write_fits(path, {"CDELT2": 0.0005})
    headers = read_sp_headers(str(path))
    assert headers["sp_pixscale"] == 1.8


def test_missing_astropy_raises_importerror(tmp_path, monkeypatch):
    import grading.fits_reader as fits_reader_mod

    monkeypatch.setattr(fits_reader_mod, "fits", None)
    with pytest.raises(ImportError, match="astropy is required"):
        fits_reader_mod.read_sp_headers(str(tmp_path / "whatever.fits"))


def test_header_float_rejects_nonfinite_values():
    assert _header_float({"VALUE": float("inf")}, "VALUE") is None
    assert _header_float({"VALUE": float("nan")}, "VALUE") is None
