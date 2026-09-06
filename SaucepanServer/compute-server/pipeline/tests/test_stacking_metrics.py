"""stacking/metrics.py — background/noise/SNR measurement and stack-quality
summary. summarize_stack_quality() is the single source of truth used by
both build_output_header() and run_stack_pipeline()'s return dict.
"""

from __future__ import annotations

import numpy as np
import pytest
from astropy.io import fits
from astropy.wcs import WCS
from saucepan_pipeline.stacking.metrics import (
    compute_snr,
    get_pixel_scale_from_header,
    measure_background_noise,
    measure_image_quality,
    summarize_stack_quality,
)
from saucepan_pipeline.stacking.models import FrameInfo, StackResult


def _tan_wcs(ra=100.0, dec=10.0, shape=(32, 32)) -> WCS:
    h, w = shape
    hdr = fits.Header()
    hdr["CTYPE1"] = "RA---TAN"
    hdr["CTYPE2"] = "DEC--TAN"
    hdr["CRPIX1"] = w / 2.0
    hdr["CRPIX2"] = h / 2.0
    hdr["CRVAL1"] = ra
    hdr["CRVAL2"] = dec
    hdr["CDELT1"] = -1.0 / 3600.0
    hdr["CDELT2"] = 1.0 / 3600.0
    return WCS(hdr)


def _frame(snr=5.0, target_snr=None, ra=None, dec=None) -> FrameInfo:
    hdr = fits.Header()
    if ra is not None:
        hdr["SP_RA"] = ra
    if dec is not None:
        hdr["SP_DEC"] = dec
    return FrameInfo(
        path="mem",
        telescope_id="t",
        data=np.zeros((4, 4)),
        header=hdr,
        wcs=_tan_wcs(),
        snr=snr,
        target_snr=target_snr,
    )


# --- measure_background_noise / measure_image_quality (delegation) --------


def test_measure_background_noise_returns_four_values() -> None:
    data = np.full((32, 32), 100.0, dtype=np.float32)
    bg, noise, star_px, sat = measure_background_noise(data)
    assert bg == pytest.approx(100.0, abs=1.0)
    assert noise >= 0.0
    assert star_px >= 0
    assert sat >= 0


def test_measure_image_quality_delegates_to_assess_quality() -> None:
    data = np.full((16, 16), 50.0, dtype=np.float32)
    result = measure_image_quality(data)
    assert "background" in result
    assert "snr" in result


# --- compute_snr: sample-size branch ---------------------------------------


def test_compute_snr_uses_percentile_with_large_sample() -> None:
    data = np.full((64, 64), 100.0, dtype=np.float32)  # 4096 px > 1000
    data[0, 0] = 5000.0
    snr = compute_snr(data, bg=100.0, noise=2.0)
    assert snr >= 0.0


def test_compute_snr_falls_back_to_bg_for_small_sample() -> None:
    """With <=1000 non-saturated pixels, p995 defaults to bg -> signal=0."""
    data = np.full((10, 10), 100.0, dtype=np.float32)  # 100 px < 1000
    snr = compute_snr(data, bg=100.0, noise=2.0)
    assert snr == 0.0


def test_compute_snr_zero_noise_returns_zero() -> None:
    data = np.full((64, 64), 100.0, dtype=np.float32)
    assert compute_snr(data, bg=100.0, noise=0.0) == 0.0


def test_compute_snr_excludes_saturated_pixels() -> None:
    data = np.full((64, 64), 70000.0, dtype=np.float32)  # all saturated
    snr = compute_snr(data, bg=100.0, noise=1.0)
    assert snr == 0.0  # p995 falls back to bg (no non-sat pixels)


# --- get_pixel_scale_from_header: priority order ----------------------------


def test_get_pixel_scale_from_header_sp_pixscale_priority() -> None:
    hdr = fits.Header()
    hdr["SP_PIXSCALE"] = 1.1
    hdr["PIXSCALE"] = 9.9
    assert get_pixel_scale_from_header(hdr) == pytest.approx(1.1)


def test_get_pixel_scale_from_header_cdelt2_fallback() -> None:
    hdr = fits.Header()
    hdr["CDELT2"] = 0.001
    assert get_pixel_scale_from_header(hdr) == pytest.approx(3.6)


def test_get_pixel_scale_from_header_cd2_2_fallback() -> None:
    hdr = fits.Header()
    hdr["CD2_2"] = 0.0002
    assert get_pixel_scale_from_header(hdr) == pytest.approx(0.72)


def test_get_pixel_scale_from_header_none_when_absent() -> None:
    assert get_pixel_scale_from_header(fits.Header()) is None


# --- summarize_stack_quality -------------------------------------------------


def _stack_result(science, noise_map=None, n_frames=2) -> StackResult:
    if noise_map is None:
        noise_map = np.full_like(science, 2.0)
    return StackResult(
        science=science,
        weight_map=np.ones_like(science),
        noise_map=noise_map,
        coverage_map=np.ones_like(science, dtype=np.int32),
        ref_wcs=_tan_wcs(shape=science.shape),
        n_frames=n_frames,
        n_rejected=0,
        provenance=[],
    )


def test_summarize_stack_quality_all_nan_science_returns_zero_defaults() -> None:
    science = np.full((16, 16), np.nan, dtype=np.float32)
    result = _stack_result(science)
    frames = [_frame(snr=5.0), _frame(snr=8.0)]
    summary = summarize_stack_quality(result, frames)
    assert summary["background"] == 0.0
    assert summary["star_pixels"] == 0


def test_summarize_stack_quality_no_ra_dec_leaves_target_fields_none() -> None:
    science = np.full((16, 16), 100.0, dtype=np.float32)
    result = _stack_result(science)
    frames = [_frame(snr=5.0), _frame(snr=8.0)]  # no ra/dec
    summary = summarize_stack_quality(result, frames)
    assert summary["stack_target_flux"] is None
    assert summary["stack_snr_target"] is None
    assert summary["efficiency_target"] is None


def test_summarize_stack_quality_malformed_radec_skipped() -> None:
    science = np.full((16, 16), 100.0, dtype=np.float32)
    result = _stack_result(science)
    hdr = fits.Header()
    hdr["SP_RA"] = "not-a-number"
    hdr["SP_DEC"] = "also-bad"
    frame = FrameInfo(
        path="mem",
        telescope_id="t",
        data=np.zeros((4, 4)),
        header=hdr,
        wcs=_tan_wcs(shape=(16, 16)),
        snr=5.0,
    )
    summary = summarize_stack_quality(result, [frame])
    assert summary["stack_target_flux"] is None


def test_summarize_stack_quality_best_single_snr_zero_when_all_frames_nonpositive() -> None:
    science = np.full((16, 16), 100.0, dtype=np.float32)
    result = _stack_result(science)
    frames = [_frame(snr=0.0), _frame(snr=-1.0)]
    summary = summarize_stack_quality(result, frames)
    assert summary["best_single_snr"] == 0.0
    assert summary["snr_gain"] == 0.0
    assert summary["efficiency"] == 0.0


def test_summarize_stack_quality_theoretical_max_matches_sqrt_n_frames() -> None:
    science = np.full((16, 16), 100.0, dtype=np.float32)
    result = _stack_result(science, n_frames=4)
    frames = [_frame(snr=5.0)]
    summary = summarize_stack_quality(result, frames)
    assert summary["theoretical_max"] == pytest.approx(2.0)


def test_summarize_stack_quality_no_finite_noise_gives_zero_stack_noise() -> None:
    science = np.full((16, 16), 100.0, dtype=np.float32)
    noise_map = np.full((16, 16), np.nan, dtype=np.float32)
    result = _stack_result(science, noise_map=noise_map)
    summary = summarize_stack_quality(result, [_frame(snr=1.0)])
    assert summary["stack_noise_adu"] == 0.0
    assert summary["stack_snr"] == 0.0


def test_summarize_stack_quality_target_anchored_fields_populated_with_radec() -> None:
    science = np.full((32, 32), 50.0, dtype=np.float32)
    yy, xx = np.mgrid[:32, :32]
    r = np.sqrt((xx - 16) ** 2 + (yy - 16) ** 2)
    science[r <= 8] += 500.0
    result = _stack_result(science)
    frames = [
        _frame(snr=5.0, target_snr=3.0, ra=100.0, dec=10.0),
        _frame(snr=6.0, target_snr=4.0, ra=100.0, dec=10.0),
    ]
    summary = summarize_stack_quality(result, frames)
    assert summary["stack_target_flux"] is not None
    assert summary["best_single_snr_target"] == pytest.approx(4.0)
