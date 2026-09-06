"""stacking/reproject.py — reference WCS selection (highest-resolution vs
median-centered grid) and per-frame reprojection onto the common grid.
"""

from __future__ import annotations

import numpy as np
import pytest
from astropy.io import fits
from astropy.wcs import WCS
from saucepan_pipeline.stacking.models import FrameInfo
from saucepan_pipeline.stacking.reproject import reproject_frame, select_reference_wcs


def _tan_wcs(ra=100.0, dec=10.0, pixel_scale_arcsec=1.0, shape=(32, 32)) -> WCS:
    h, w = shape
    hdr = fits.Header()
    hdr["CTYPE1"] = "RA---TAN"
    hdr["CTYPE2"] = "DEC--TAN"
    hdr["CRPIX1"] = w / 2.0
    hdr["CRPIX2"] = h / 2.0
    hdr["CRVAL1"] = ra
    hdr["CRVAL2"] = dec
    hdr["CDELT1"] = -pixel_scale_arcsec / 3600.0
    hdr["CDELT2"] = pixel_scale_arcsec / 3600.0
    return WCS(hdr)


def _frame(pixel_scale, shape=(32, 32), ra=100.0, dec=10.0, tele="t") -> FrameInfo:
    hdr = fits.Header()
    hdr["SP_RA"] = ra
    hdr["SP_DEC"] = dec
    data = np.full(shape, 100.0, dtype=np.float32)
    return FrameInfo(
        path="mem",
        telescope_id=tele,
        data=data,
        header=hdr,
        wcs=_tan_wcs(ra=ra, dec=dec, pixel_scale_arcsec=pixel_scale, shape=shape),
        pixel_scale_arcsec=pixel_scale,
    )


# --- select_reference_wcs: highest-resolution mode (default) --------------


def test_select_reference_wcs_picks_smallest_pixel_scale() -> None:
    frames = [_frame(2.0, tele="wide"), _frame(0.5, tele="sharp"), _frame(1.0, tele="mid")]
    wcs, shape = select_reference_wcs(frames, use_highest_resolution=True)
    # The sharp (0.5"/px) frame's WCS should be selected.
    assert wcs.wcs.cdelt[1] == pytest.approx(0.5 / 3600.0, rel=1e-6)


def test_select_reference_wcs_ignores_frames_with_zero_pixel_scale() -> None:
    # select_reference_wcs filters on FrameInfo.pixel_scale_arcsec (metadata
    # field), independent of what's baked into the WCS object itself, so the
    # "bad" frame's WCS can carry a normal (non-singular) scale.
    bad = _frame(1.0, tele="bad")
    bad.pixel_scale_arcsec = 0.0
    good = _frame(1.5, tele="good")
    wcs, shape = select_reference_wcs([bad, good], use_highest_resolution=True)
    assert wcs.wcs.cdelt[1] == pytest.approx(1.5 / 3600.0, rel=1e-6)


def test_select_reference_wcs_empty_frames_falls_through_without_crash() -> None:
    # use_highest_resolution path requires len(frames) > 0 to engage; with an
    # empty list it falls through to the pixel_scales/ras loops, which are
    # also empty, and then `frames[0]` would raise -- documents that this
    # function assumes a non-empty frame list (matches stack_frames' own
    # upstream guard: `if len(frames) == 0: raise ValueError`).
    with pytest.raises(IndexError):
        select_reference_wcs([], use_highest_resolution=True)


# --- select_reference_wcs: median/centered mode -----------------------------


def test_select_reference_wcs_median_mode_centers_on_mean_radec() -> None:
    frames = [_frame(1.0, ra=100.0, dec=10.0), _frame(1.0, ra=102.0, dec=12.0)]
    wcs, shape = select_reference_wcs(frames, use_highest_resolution=False)
    assert wcs.wcs.crval[0] == pytest.approx(101.0, abs=0.01)
    assert wcs.wcs.crval[1] == pytest.approx(11.0, abs=0.01)
    assert shape[0] >= 512 and shape[1] >= 512  # min output size floor


def test_select_reference_wcs_median_mode_uses_median_pixel_scale() -> None:
    frames = [_frame(0.5), _frame(1.0), _frame(2.0)]
    wcs, shape = select_reference_wcs(frames, use_highest_resolution=False)
    assert abs(wcs.wcs.cdelt[1]) == pytest.approx(1.0 / 3600.0, rel=1e-6)


def test_select_reference_wcs_median_mode_no_valid_radec_uses_first_frame() -> None:
    """When no frame has a resolvable SP_RA/SP_DEC, fall back to the first
    frame's own WCS/shape rather than crashing on an empty ras/decs list."""
    hdr = fits.Header()  # no SP_RA/SP_DEC
    data = np.zeros((16, 16), dtype=np.float32)
    frame = FrameInfo(
        path="mem",
        telescope_id="t",
        data=data,
        header=hdr,
        wcs=_tan_wcs(shape=(16, 16)),
        pixel_scale_arcsec=1.0,
    )
    wcs, shape = select_reference_wcs([frame], use_highest_resolution=False)
    assert shape == (16, 16)


def test_select_reference_wcs_median_mode_skips_zero_zero_radec() -> None:
    """RA=0, Dec=0 is treated as 'not set' (the `if ra != 0.0 or dec != 0.0`
    guard) — a real target could coincidentally sit there, but this documents
    existing behavior rather than changing it."""
    frames = [_frame(1.0, ra=0.0, dec=0.0)]
    wcs, shape = select_reference_wcs(frames, use_highest_resolution=False)
    # No valid ra/dec found -> falls back to frames[0]'s own WCS/shape.
    assert shape == frames[0].data.shape


def test_select_reference_wcs_median_mode_malformed_radec_is_skipped() -> None:
    hdr = fits.Header()
    hdr["SP_RA"] = "not-a-number"
    hdr["SP_DEC"] = "also-not-a-number"
    data = np.zeros((8, 8), dtype=np.float32)
    frame = FrameInfo(
        path="mem",
        telescope_id="t",
        data=data,
        header=hdr,
        wcs=_tan_wcs(shape=(8, 8)),
        pixel_scale_arcsec=1.0,
    )
    wcs, shape = select_reference_wcs([frame], use_highest_resolution=False)
    assert shape == (8, 8)  # fell back to frames[0]


# --- reproject_frame ---------------------------------------------------------


def test_reproject_frame_returns_bool_footprint_and_finite_data() -> None:
    frame = _frame(1.0, shape=(32, 32))
    target_wcs = _tan_wcs(ra=100.0, dec=10.0, pixel_scale_arcsec=1.0, shape=(32, 32))
    result, footprint = reproject_frame(frame, target_wcs, (32, 32))
    assert result.dtype == np.float32
    assert footprint.dtype == bool
    assert np.all(np.isfinite(result))


def test_reproject_frame_no_overlap_returns_zero_footprint() -> None:
    frame = _frame(1.0, ra=10.0, dec=80.0, shape=(16, 16))
    target_wcs = _tan_wcs(ra=250.0, dec=-60.0, pixel_scale_arcsec=1.0, shape=(16, 16))
    result, footprint = reproject_frame(frame, target_wcs, (16, 16))
    assert footprint.sum() == 0
