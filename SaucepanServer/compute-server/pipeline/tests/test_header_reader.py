"""header_reader.py — extracts SP_* headers only (never raw instrument
headers), per the pipeline's FITS contract.
"""

from __future__ import annotations

from pathlib import Path

import numpy as np
from astropy.io import fits
from saucepan_pipeline.header_reader import SPHeaders, read_sp_headers, validate_headers


def _write(path: Path, **hdr_kv) -> None:
    hdu = fits.PrimaryHDU(data=np.zeros((4, 4), dtype=np.float32))
    for k, v in hdr_kv.items():
        hdu.header[k] = v
    hdu.writeto(path, overwrite=True)


def test_read_sp_headers_full_set(tmp_path: Path) -> None:
    p = tmp_path / "f.fits"
    _write(
        p,
        SP_SCTYPE="photometry",
        SP_FILTER=" V ",
        SP_CALSTAT="BDF",
        SP_TIER=1,
        SP_RA=180.0,
        SP_DEC=-10.0,
        SP_EXPTIME=30.0,
        SP_PIXSCALE=1.2,
        SP_FWHM=3.5,
    )
    result = read_sp_headers(str(p))
    assert result.science_type == "photometry"
    assert result.filter_name == "V"
    assert result.calstat == "BDF"
    assert result.tier == 1
    assert result.ra == 180.0
    assert result.dec == -10.0
    assert result.exposure_time == 30.0
    assert result.pixscale == 1.2
    assert result.fwhm == 3.5


def test_read_sp_headers_missing_all_uses_defaults(tmp_path: Path) -> None:
    p = tmp_path / "f.fits"
    _write(p)
    result = read_sp_headers(str(p))
    assert result == SPHeaders()  # all defaults: tier=3, filter=L, etc.


def test_read_sp_headers_ignores_raw_instrument_headers(tmp_path: Path) -> None:
    """Contract check: raw (non-SP_) headers must never leak into SPHeaders,
    even when they share a semantic meaning with an SP_ field."""
    p = tmp_path / "f.fits"
    _write(p, FILTER="Ha", RA=99.0, DEC=5.0, EXPTIME=10.0)  # raw, no SP_ prefix
    result = read_sp_headers(str(p))
    assert result.filter_name == "L"  # default, NOT "Ha" from raw FILTER
    assert result.ra is None
    assert result.dec is None
    assert result.exposure_time is None


def test_read_sp_headers_missing_file_returns_defaults_and_logs(tmp_path: Path) -> None:
    result = read_sp_headers(str(tmp_path / "does_not_exist.fits"))
    assert result.tier == 3
    assert result.ra is None


def test_read_sp_headers_partial_headers(tmp_path: Path) -> None:
    p = tmp_path / "f.fits"
    _write(p, SP_RA=1.0, SP_DEC=2.0)
    result = read_sp_headers(str(p))
    assert result.ra == 1.0
    assert result.dec == 2.0
    assert result.tier == 3  # not present -> default
    assert result.filter_name == "L"


def test_validate_headers_all_valid() -> None:
    headers = SPHeaders(tier=1, ra=1.0, dec=2.0, filter_name="V")
    ok, errors = validate_headers(headers)
    assert ok is True
    assert errors == []


def test_validate_headers_tier3_flagged() -> None:
    headers = SPHeaders(tier=3, ra=1.0, dec=2.0, filter_name="V")
    ok, errors = validate_headers(headers)
    assert ok is False
    assert any("tier-3" in e for e in errors)


def test_validate_headers_missing_ra_dec() -> None:
    headers = SPHeaders(tier=1, ra=None, dec=None, filter_name="V")
    ok, errors = validate_headers(headers)
    assert ok is False
    assert any("coordinates" in e for e in errors)


def test_validate_headers_missing_filter() -> None:
    headers = SPHeaders(tier=1, ra=1.0, dec=2.0, filter_name=None)
    ok, errors = validate_headers(headers)
    assert ok is False
    assert any("filter" in e.lower() for e in errors)


def test_validate_headers_accumulates_multiple_errors() -> None:
    headers = SPHeaders(tier=3, ra=None, dec=None, filter_name=None)
    ok, errors = validate_headers(headers)
    assert ok is False
    assert len(errors) == 3
