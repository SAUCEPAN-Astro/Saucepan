"""Regression test for #467: combine.py's noise_map must reflect the
actual combined noise, not be inflated by the unrelated FWHM-weighting
factor folded into the same weight used to combine pixel values.
"""

from __future__ import annotations

import zlib

import numpy as np
from astropy.io import fits
from astropy.wcs import WCS
from saucepan_pipeline.stacking.combine import stack_frames
from saucepan_pipeline.stacking.models import FrameInfo


def _make_frame(telescope_id: str, noise_adu: float, fwhm_arcsec: float) -> FrameInfo:
    h, w = 64, 64
    rng = np.random.default_rng(zlib.crc32(telescope_id.encode()))
    data = rng.normal(0.0, noise_adu, size=(h, w)).astype(np.float32)
    data[30:34, 30:34] += 500.0  # shared bright star, same for every frame

    wcs = WCS(naxis=2)
    wcs.wcs.crpix = [w / 2.0, h / 2.0]
    wcs.wcs.crval = [180.0, 0.0]
    wcs.wcs.cdelt = [-1.0 / 3600.0, 1.0 / 3600.0]
    wcs.wcs.ctype = ["RA---TAN", "DEC--TAN"]

    header = fits.Header()
    header.update(wcs.to_header())

    return FrameInfo(
        path=f"/tmp/{telescope_id}.fits",
        telescope_id=telescope_id,
        data=data,
        header=header,
        wcs=wcs,
        noise_adu=noise_adu,
        background=0.0,
        snr=500.0 / noise_adu,
        fwhm_arcsec=fwhm_arcsec,
        pixel_scale_arcsec=1.0,
        exptime=30.0,
    )


def test_noise_map_matches_theory_for_equal_quality_frames():
    """Two frames, identical noise and identical (already PSF-matched)
    FWHM - the physically correct combined noise is noise/sqrt(2),
    regardless of whether FWHM-weighting is enabled, since both frames
    get the same FWHM weight factor and it should cancel out of the
    variance calculation, not inflate it."""
    noise_adu = 16.0
    frames = [
        _make_frame("tele-a", noise_adu, fwhm_arcsec=3.0),
        _make_frame("tele-b", noise_adu, fwhm_arcsec=3.0),
    ]

    result = stack_frames(frames, weight_by_fwhm=True, photometric_scale=False, sigma_clip=0.0)

    valid_noise = result.noise_map[np.isfinite(result.noise_map)]
    median_noise = float(np.median(valid_noise))
    expected = noise_adu / np.sqrt(2)

    assert 0.85 * expected < median_noise < 1.15 * expected, (
        f"noise_map={median_noise:.3f} should be close to noise/sqrt(2)={expected:.3f} "
        f"for two equal-quality, equal-FWHM frames - if this is inflated, the FWHM "
        f"weighting factor is corrupting the noise_map formula again (#467)"
    )


def test_noise_map_without_fwhm_weight_matches_with_fwhm_weight():
    """The core #467 regression: for frames with identical FWHM (already
    PSF-matched, as production frames always are by this stage), toggling
    weight_by_fwhm must not change the resulting noise_map - it did before
    the fix, because the (wrong) sqrt(1/weight_sum) formula is sensitive to
    the absolute scale of the weight, not just its relative distribution."""
    frames_a = [
        _make_frame("tele-a", 16.0, fwhm_arcsec=3.0),
        _make_frame("tele-b", 16.0, fwhm_arcsec=3.0),
    ]
    frames_b = [
        _make_frame("tele-a", 16.0, fwhm_arcsec=3.0),
        _make_frame("tele-b", 16.0, fwhm_arcsec=3.0),
    ]

    result_with = stack_frames(
        frames_a, weight_by_fwhm=True, photometric_scale=False, sigma_clip=0.0
    )
    result_without = stack_frames(
        frames_b, weight_by_fwhm=False, photometric_scale=False, sigma_clip=0.0
    )

    noise_with = float(np.median(result_with.noise_map[np.isfinite(result_with.noise_map)]))
    noise_without = float(
        np.median(result_without.noise_map[np.isfinite(result_without.noise_map)])
    )

    assert 0.9 * noise_without < noise_with < 1.1 * noise_without, (
        f"noise_map with FWHM-weighting ({noise_with:.3f}) diverged from without "
        f"({noise_without:.3f}) despite identical per-frame FWHM - the FWHM factor "
        f"is leaking into the noise estimate again"
    )
