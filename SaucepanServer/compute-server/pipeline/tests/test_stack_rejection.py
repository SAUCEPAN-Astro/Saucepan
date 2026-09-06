"""#411 - iterative per-pixel sigma-clip across the frame axis in
``stacking/combine.py``.

The old rejection was a single-pass, per-frame running-mean comparison
that (a) never clipped frame 0, (b) used a scalar threshold, and (c) only
ever discarded a whole frame once >50% of its footprint deviated - it
never masked an individual pixel. These tests pin the replacement:

* a hot pixel present in exactly one frame is masked in that frame only;
* a cosmic-ray streak in *frame 0* is now rejected (the old special case
  is gone);
* rejecting a contaminated frame's bad pixels improves the stack SNR;
* a clean cube produces zero rejections and a result identical to the
  plain weighted mean (``sigma_clip=0``).
"""

from __future__ import annotations

import numpy as np
from astropy.io import fits
from astropy.wcs import WCS
from saucepan_pipeline.stacking.combine import stack_frames
from saucepan_pipeline.stacking.models import FrameInfo

SHAPE = (48, 48)


def _wcs() -> WCS:
    h, w = SHAPE
    hdr = fits.Header()
    hdr["CTYPE1"] = "RA---TAN"
    hdr["CTYPE2"] = "DEC--TAN"
    hdr["CRPIX1"] = w / 2.0
    hdr["CRPIX2"] = h / 2.0
    hdr["CRVAL1"] = 180.0
    hdr["CRVAL2"] = 0.0
    hdr["CDELT1"] = -1.0 / 3600.0
    hdr["CDELT2"] = 1.0 / 3600.0
    return WCS(hdr)


def _star(amp: float = 800.0) -> np.ndarray:
    yy, xx = np.mgrid[0 : SHAPE[0], 0 : SHAPE[1]]
    cy, cx = 24.0, 24.0
    return amp * np.exp(-((yy - cy) ** 2 + (xx - cx) ** 2) / (2.0 * 2.5**2))


def _frame(data: np.ndarray, tele: str, noise_adu: float = 10.0) -> FrameInfo:
    wcs = _wcs()
    hdr = fits.Header()
    hdr.update(wcs.to_header())
    return FrameInfo(
        path=f"mem:{tele}",
        telescope_id=tele,
        data=np.asarray(data, dtype=np.float32),
        header=hdr,
        wcs=wcs,
        noise_adu=noise_adu,
        background=100.0,
        snr=50.0,
        fwhm_arcsec=3.0,
        pixel_scale_arcsec=1.0,
        exptime=30.0,
    )


def _clean_base() -> np.ndarray:
    """Noiseless background + star - identical for every frame, so the
    only cross-frame outliers are the ones a test injects."""
    return np.full(SHAPE, 100.0, dtype=np.float64) + _star()


def _stack(frames, **kw):
    defaults = dict(
        weight_by_fwhm=False,
        photometric_scale=False,
        auto_crop=False,
    )
    defaults.update(kw)
    return stack_frames(frames, **defaults)


def test_single_frame_hot_pixel_is_masked_in_that_frame_only() -> None:
    base = _clean_base()
    hot_y, hot_x = 8, 40  # well away from the star
    frames = [_frame(base.copy(), f"t{i}") for i in range(4)]
    frames[2].data[hot_y, hot_x] += 5.0e5  # single deviant pixel, frame 2 only

    result = _stack(frames, sigma_clip=3.0)

    prov = result.provenance
    assert prov[2]["n_rejected_pixels"] == 1, prov
    assert all(prov[i]["n_rejected_pixels"] == 0 for i in (0, 1, 3)), prov
    # No frame is dropped wholesale for a single bad pixel.
    assert all(not p["rejected"] for p in prov)
    assert all(p["clip_iterations"] >= 1 for p in prov)
    # The hot pixel never reaches the science plane.
    assert abs(result.science[hot_y, hot_x] - 100.0) < 5.0


def test_cosmic_ray_streak_in_frame_zero_is_rejected() -> None:
    """The old code unconditionally trusted frame 0. A streak there must
    now be clipped like it would be in any other frame."""
    base = _clean_base()
    frames = [_frame(base.copy(), f"t{i}") for i in range(4)]
    streak = (slice(4, 5), slice(10, 24))  # 14-pixel horizontal streak
    frames[0].data[streak] += 4.0e4

    result = _stack(frames, sigma_clip=3.0)

    prov = result.provenance
    assert prov[0]["n_rejected_pixels"] == 14, prov
    assert all(prov[i]["n_rejected_pixels"] == 0 for i in (1, 2, 3)), prov
    assert not prov[0]["rejected"]  # 14 px is far below min_weight_fraction
    # Science along the streak matches the clean sky, not the streak.
    assert np.allclose(result.science[streak], 100.0, atol=5.0)


def test_stack_fidelity_improves_when_outlier_frame_is_rejected() -> None:
    """Including a contaminated frame drags the combined image away from
    the true sky; rejecting its bad pixels restores it, so the stack's
    per-pixel SNR (signal over departure-from-truth) improves."""
    rng = np.random.default_rng(411)
    star = _star()
    truth = 100.0 + star
    frames = []
    for i in range(4):
        data = truth + rng.normal(0.0, 10.0, size=SHAPE)
        frames.append(_frame(data, f"t{i}"))
    # Frame 3 carries a bright contamination blob in an otherwise blank
    # patch (bottom-left), nowhere near the star at (24, 24).
    blob = (slice(34, 46), slice(2, 14))
    frames[3].data[blob] += 6000.0

    with_clip = _stack(frames, sigma_clip=3.0)
    no_clip = _stack(frames, sigma_clip=0.0)

    # The blob biases the un-clipped combination hard; clipping removes it.
    bias_clip = abs(float(np.nanmedian(with_clip.science[blob])) - 100.0)
    bias_noclip = abs(float(np.nanmedian(no_clip.science[blob])) - 100.0)
    assert bias_noclip > 500.0
    assert bias_clip < 10.0

    # Whole-frame RMS error vs the known truth drops.
    rms_clip = float(np.sqrt(np.nanmean((with_clip.science - truth) ** 2)))
    rms_noclip = float(np.sqrt(np.nanmean((no_clip.science - truth) ** 2)))
    assert rms_clip < 0.2 * rms_noclip

    # The star peak itself (at pixel (24, 24)) is untouched by the clip -
    # global max would just re-find the blob in the un-clipped image.
    star_box = (slice(20, 29), slice(20, 29))
    peak_clip = float(np.nanmax(with_clip.science[star_box]))
    peak_noclip = float(np.nanmax(no_clip.science[star_box]))
    assert abs(peak_clip - peak_noclip) / peak_noclip < 0.05

    # Net effect: SNR = star-peak / RMS-error improves.
    assert peak_clip / rms_clip > peak_noclip / rms_noclip
    assert with_clip.provenance[3]["n_rejected_pixels"] >= 100


def test_clean_cube_has_zero_rejections_and_equals_the_weighted_mean() -> None:
    base = _clean_base()
    frames = [_frame(base.copy(), f"t{i}", noise_adu=8.0 + i) for i in range(4)]

    clipped = _stack(frames, sigma_clip=3.0)
    plain = _stack(frames, sigma_clip=0.0)

    for p in clipped.provenance:
        assert p["n_rejected_pixels"] == 0
        assert not p["rejected"]
        assert p["reject_reason"] == ""
        assert p["clip_iterations"] == 1  # entered the loop, converged at once
    assert clipped.n_rejected == 0
    # Identical result to the plain weighted mean (no clipping path).
    np.testing.assert_allclose(clipped.science, plain.science, rtol=0, atol=1e-6)
    np.testing.assert_allclose(
        clipped.noise_map, plain.noise_map, rtol=0, atol=1e-6, equal_nan=True
    )
    # And it really is the weighted mean of the (identical) inputs.
    np.testing.assert_allclose(clipped.science, base, rtol=0, atol=1e-4)
