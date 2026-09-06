"""psf_match.py — PSF matching for heterogeneous telescope data.

Must run in native pixel space, before reprojection (pipeline stage 4 of 6).
Covers match_psf's ValueError guard (cannot sharpen PSF), select_target_psf,
write_psf_headers, and the Lambda handler().
"""

from __future__ import annotations

import base64

import numpy as np
import pytest
from astropy.io import fits
from saucepan_pipeline.psf_match import (
    handler,
    match_psf,
    select_target_psf,
    write_psf_headers,
)


def _star_image(size=64, fwhm_px=3.0, seed=0) -> np.ndarray:
    rng = np.random.default_rng(seed)
    data = np.full((size, size), 100.0, dtype=np.float32)
    data += rng.normal(0, 1.0, size=(size, size)).astype(np.float32)
    sigma = fwhm_px / 2.3548
    yy, xx = np.mgrid[:size, :size]
    data += (
        500.0 * np.exp(-(((xx - size / 2) ** 2 + (yy - size / 2) ** 2) / (2 * sigma**2)))
    ).astype(np.float32)
    return data


# --- match_psf: normal operation and ValueError guard -----------------------


def test_match_psf_broadens_narrower_source_to_target() -> None:
    data = _star_image(fwhm_px=2.0)
    result = match_psf(data, source_fwhm_arcsec=2.0, target_fwhm_arcsec=5.0, pixel_scale_arcsec=1.0)
    assert result.shape == data.shape
    assert result.dtype == np.float32


def test_match_psf_raises_when_source_equals_target() -> None:
    data = _star_image()
    with pytest.raises(ValueError, match="Cannot sharpen"):
        match_psf(data, source_fwhm_arcsec=3.0, target_fwhm_arcsec=3.0, pixel_scale_arcsec=1.0)


def test_match_psf_raises_when_source_exceeds_target() -> None:
    data = _star_image()
    with pytest.raises(ValueError, match="Cannot sharpen"):
        match_psf(data, source_fwhm_arcsec=6.0, target_fwhm_arcsec=3.0, pixel_scale_arcsec=1.0)


def test_match_psf_preserves_approx_total_flux() -> None:
    """Convolution should conserve flux (within numerical tolerance)."""
    data = _star_image(fwhm_px=2.0, seed=1) - 100.0  # background-subtracted
    result = match_psf(data, source_fwhm_arcsec=2.0, target_fwhm_arcsec=4.0, pixel_scale_arcsec=1.0)
    assert result.sum() == pytest.approx(data.sum(), rel=0.05)


# --- select_target_psf -------------------------------------------------------


def test_select_target_psf_returns_max() -> None:
    assert select_target_psf([2.0, 5.5, 3.1]) == 5.5


def test_select_target_psf_single_value() -> None:
    assert select_target_psf([4.2]) == 4.2


def test_select_target_psf_empty_list_raises() -> None:
    with pytest.raises(ValueError):
        select_target_psf([])


def test_select_target_psf_extreme_fwhm_values() -> None:
    """With fewer than 4 frames there is no peer group to judge an outlier
    against, so a pathological value still dominates (documents the retained
    small-N max() behaviour)."""
    assert select_target_psf([1.0, 1.1, 999.0]) == 999.0


def test_select_target_psf_drops_lone_upward_outlier() -> None:
    """4+ frames, one FWHM mis-measured high (#480): the epoch target must
    stay anchored to the real fleet seeing, not the outlier."""
    fleet = [2.70, 2.80, 2.75, 2.90]
    assert select_target_psf(fleet + [16.44]) == 2.90  # outlier rejected
    assert select_target_psf(fleet) == 2.90  # unchanged without it


def test_select_target_psf_keeps_corroborated_bad_seeing() -> None:
    """Two frames well above the rest corroborate each other as genuine bad
    seeing — they are NOT rejected."""
    assert select_target_psf([2.7, 2.8, 2.9, 5.4, 5.6]) == 5.6


def test_select_target_psf_no_rejection_when_spread_is_tight() -> None:
    """A fleet with a normal spread and no outlier resolves to its max."""
    assert select_target_psf([2.6, 2.8, 3.0, 3.3, 3.5]) == 3.5


# --- write_psf_headers --------------------------------------------------------


def test_write_psf_headers_sets_expected_keys() -> None:
    header = fits.Header()
    write_psf_headers(header, source_fwhm=2.0, target_fwhm=4.0, kernel_sigma_px=1.5)
    assert header["SP_PSF_MATCH"] is True
    assert header["SP_PSF_IN"] == pytest.approx(2.0)
    assert header["SP_PSF_OUT"] == pytest.approx(4.0)
    assert header["SP_PSF_KSIG"] == pytest.approx(1.5)
    assert header["SP_PSF_METH"] == "gaussian-fft"


# --- Lambda handler ------------------------------------------------------------


def test_handler_unknown_action_returns_400() -> None:
    result = handler({"action": "nope"})
    assert result["statusCode"] == 400


def test_handler_round_trips_match_psf() -> None:
    data = _star_image(fwhm_px=2.0, seed=2)
    payload = {
        "action": "match_psf",
        "data_base64": base64.b64encode(data.tobytes()).decode(),
        "shape": list(data.shape),
        "source_fwhm": 2.0,
        "target_fwhm": 5.0,
        "pixel_scale": 1.0,
    }
    result = handler(payload)
    assert result["statusCode"] == 200
    assert result["body"]["shape"] == list(data.shape)
    assert result["body"]["metadata"]["psf_matched"] is True
    out_bytes = base64.b64decode(result["body"]["data_base64"])
    out = np.frombuffer(out_bytes, dtype=np.float32).reshape(data.shape)
    assert out.shape == data.shape


def test_handler_error_when_source_exceeds_target_returns_500() -> None:
    data = _star_image()
    payload = {
        "action": "match_psf",
        "data_base64": base64.b64encode(data.tobytes()).decode(),
        "shape": list(data.shape),
        "source_fwhm": 9.0,
        "target_fwhm": 3.0,
        "pixel_scale": 1.0,
    }
    result = handler(payload)
    assert result["statusCode"] == 500
    assert "error" in result["body"]


def test_handler_missing_required_field_returns_500() -> None:
    result = handler({"action": "match_psf", "data_base64": "", "shape": [1, 1]})
    assert result["statusCode"] == 500
