"""#412 - flux-conserving reprojection + per-pixel inverse-variance weights.

Covers the three regressions the issue calls out:

1. A synthetic flat field keeps its total flux (within 1e-3) through
   ``reproject_frame`` + ``stack_frames`` - i.e. the default reprojection
   is ``reproject_exact`` (area-conserving), not bilinear interpolation.
2. A target pixel with no input coverage stays ``NaN`` in the reprojected
   array and in the stacked SCIENCE plane - it is no longer zero-filled by
   ``nan_to_num`` and injected as real 0.0 signal.
3. A frame carrying a hot (huge-variance) column contributes ~0 weight in
   that column and finite, positive weight everywhere else - the weight map
   is now built per pixel from the reprojected variance.
"""

from __future__ import annotations

import numpy as np
from astropy.io import fits
from astropy.wcs import WCS
from saucepan_pipeline.stacking import combine
from saucepan_pipeline.stacking.combine import stack_frames
from saucepan_pipeline.stacking.models import FrameInfo
from saucepan_pipeline.stacking.reproject import reproject_frame


def _tan_wcs(ra=180.0, dec=0.0, pixel_scale_arcsec=1.0, shape=(64, 64), crpix_shift=(0.0, 0.0)):
    h, w = shape
    hdr = fits.Header()
    hdr["CTYPE1"] = "RA---TAN"
    hdr["CTYPE2"] = "DEC--TAN"
    hdr["CRPIX1"] = w / 2.0 + crpix_shift[0]
    hdr["CRPIX2"] = h / 2.0 + crpix_shift[1]
    hdr["CRVAL1"] = ra
    hdr["CRVAL2"] = dec
    hdr["CDELT1"] = -pixel_scale_arcsec / 3600.0
    hdr["CDELT2"] = pixel_scale_arcsec / 3600.0
    return WCS(hdr)


def _frame(data, wcs, *, tele="t", noise_adu=5.0, fwhm=3.0, ps=1.0) -> FrameInfo:
    hdr = fits.Header()
    hdr["SP_RA"] = float(wcs.wcs.crval[0])
    hdr["SP_DEC"] = float(wcs.wcs.crval[1])
    return FrameInfo(
        path="mem",
        telescope_id=tele,
        data=np.asarray(data, dtype=np.float32),
        header=hdr,
        wcs=wcs,
        noise_adu=noise_adu,
        fwhm_arcsec=fwhm,
        pixel_scale_arcsec=ps,
        exptime=1.0,
    )


def test_reproject_exact_conserves_total_flux_of_a_localized_source() -> None:
    """A compact flux blob keeps its integrated counts through a sub-pixel
    shift - the signature of area-conserving (reproject_exact) resampling."""
    src = np.zeros((80, 80), dtype=np.float32)
    src[30:50, 30:50] = 50.0  # total = 20 * 20 * 50 = 20000, well clear of edges
    src_wcs = _tan_wcs(shape=(80, 80))
    tgt_wcs = _tan_wcs(shape=(80, 80), crpix_shift=(0.3, -0.4))  # sub-pixel offset

    result, valid = reproject_frame(_frame(src, src_wcs), tgt_wcs, (80, 80))

    total_in = float(src.sum())
    total_out = float(np.nansum(result))
    assert abs(total_out - total_in) / total_in < 1e-3
    # NaN is preserved, not zero-filled.
    assert np.isfinite(result[valid]).all()


def test_flat_field_flux_conserved_through_reproject_and_combine() -> None:
    flat_value = 120.0
    base_wcs = _tan_wcs(shape=(64, 64))
    frames = [
        _frame(np.full((64, 64), flat_value, np.float32), base_wcs, tele="a"),
        _frame(
            np.full((64, 64), flat_value, np.float32),
            _tan_wcs(shape=(64, 64), crpix_shift=(0.35, 0.2)),
            tele="b",
        ),
    ]

    result = stack_frames(
        frames,
        sigma_clip=0.0,
        weight_by_fwhm=False,
        photometric_scale=False,
        auto_crop=False,
    )

    interior = result.science[10:-10, 10:-10]
    assert np.all(np.isfinite(interior))
    assert abs(float(np.mean(interior)) / flat_value - 1.0) < 1e-3


def test_scalar_variance_reuses_data_coverage(monkeypatch) -> None:
    """A scalar-noise frame needs no second full-grid reprojection."""
    wcs = _tan_wcs(shape=(32, 32))
    frame = _frame(np.full((32, 32), 100.0, dtype=np.float32), wcs, noise_adu=5.0)
    calls = 0
    original = combine.reproject_variance

    def count_variance_reprojects(*args, **kwargs):
        nonlocal calls
        calls += 1
        return original(*args, **kwargs)

    monkeypatch.setattr(combine, "reproject_variance", count_variance_reprojects)
    result = stack_frames(
        [frame],
        sigma_clip=0.0,
        weight_by_fwhm=False,
        photometric_scale=False,
        auto_crop=False,
    )

    assert calls == 0
    assert np.allclose(result.noise_map[8:-8, 8:-8], 5.0)


def test_uncovered_target_pixels_stay_nan_not_zero() -> None:
    src = np.full((32, 32), 100.0, dtype=np.float32)
    src_wcs = _tan_wcs(ra=180.0, dec=0.0, shape=(32, 32))
    # Target grid centered ~40 px away on the sky -> only partial overlap.
    tgt_wcs = _tan_wcs(ra=180.0 + (40 / 3600.0), dec=0.0, shape=(32, 32))

    result, valid = reproject_frame(_frame(src, src_wcs), tgt_wcs, (32, 32))

    assert np.isnan(result).any(), "uncovered pixels must be NaN, not 0.0"
    uncovered = ~valid
    assert uncovered.any()
    assert np.all(np.isnan(result[uncovered]))
    assert not np.any(result[uncovered] == 0.0)


def test_hot_column_contributes_zero_weight_there_finite_elsewhere() -> None:
    wcs = _tan_wcs(shape=(48, 48))
    data = np.full((48, 48), 200.0, dtype=np.float32)
    frame = _frame(data, wcs, noise_adu=5.0)
    # Per-pixel variance map with one detector column flagged unusable.
    var = np.full((48, 48), 25.0, dtype=np.float64)  # noise_adu**2
    hot_col = 20
    var[:, hot_col] = 1.0e20
    frame.variance = var

    result = stack_frames(
        [frame],
        sigma_clip=0.0,
        weight_by_fwhm=False,
        photometric_scale=False,
        auto_crop=False,
    )

    wmap = result.weight_map
    assert np.all(np.isfinite(wmap))
    good_cols = [c for c in range(48) if c != hot_col]
    assert np.all(wmap[:, good_cols] > 0.0)
    # Hot column weight is driven to ~0 by its 1e20 variance.
    assert np.max(wmap[:, hot_col]) < 1.0e-12 * float(np.median(wmap[:, good_cols]))
