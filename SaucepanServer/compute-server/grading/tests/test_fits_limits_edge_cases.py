"""Additional edge cases for grading.fits_limits guard functions."""

from __future__ import annotations

import numpy as np
import pytest
from grading.fits_limits import (
    FitsSizeLimitError,
    check_fits_file_size,
    max_fits_bytes,
    max_fits_pixels,
    pixel_count_from_header,
)


def test_max_fits_pixels_invalid_env_falls_back_to_default(monkeypatch):
    monkeypatch.setenv("MAX_FITS_PIXELS", "not-an-int")
    assert max_fits_pixels() == 64_000_000


def test_max_fits_bytes_blank_env_falls_back_to_default(monkeypatch):
    monkeypatch.setenv("MAX_FITS_BYTES", "")
    assert max_fits_bytes() == 512 * 1024 * 1024


def test_pixel_count_from_header_naxis_zero_returns_zero():
    hdr = {"NAXIS": 0}
    assert pixel_count_from_header(hdr) == 0


def test_pixel_count_from_header_negative_naxis_returns_zero():
    hdr = {"NAXIS": -1}
    assert pixel_count_from_header(hdr) == 0


def test_pixel_count_from_header_missing_naxis_key_returns_zero():
    hdr = {"NAXIS": 2, "NAXIS1": 10}  # NAXIS2 missing
    assert pixel_count_from_header(hdr) == 0


def test_pixel_count_from_header_zero_dim_returns_zero():
    hdr = {"NAXIS": 2, "NAXIS1": 10, "NAXIS2": 0}
    assert pixel_count_from_header(hdr) == 0


def test_pixel_count_from_header_non_numeric_naxis_returns_zero():
    hdr = {"NAXIS": "not-a-number"}
    assert pixel_count_from_header(hdr) == 0


def test_pixel_count_from_header_non_numeric_dim_returns_zero():
    hdr = {"NAXIS": 1, "NAXIS1": "garbage"}
    assert pixel_count_from_header(hdr) == 0


def test_check_fits_file_size_missing_file_raises():
    with pytest.raises(FitsSizeLimitError, match="Cannot stat"):
        check_fits_file_size("/definitely/does/not/exist.fits")
