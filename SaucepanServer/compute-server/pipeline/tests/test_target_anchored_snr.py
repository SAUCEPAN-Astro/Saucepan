"""Position-anchored aperture SNR/efficiency regression (#471).

quality.py::assess_quality()'s adaptive-threshold "signal_adu" and
combine.py::estimate_photometric_scales()'s whole-image 95th-percentile flux
proxy both have no notion of *which* pixel is the actual science target -
they pick up whatever crosses a brightness threshold, which conflates the
target with field stars. An independent audit (see issue #471's filed
comment) recomputed a negative-control (zero injected signal) stacking sweep
and found estimate_photometric_scales()'s per-frame scale factor swinging
0.489-1.584 across epochs of the same fixed 4-node fleet, from field-star
contamination alone.

This test builds a self-contained synthetic scenario reproducing that same
mechanism - a coherent target at a fixed, known sky position plus
independently-placed-per-frame field stars that don't co-register when
stacked (same idea as
``validation/injection_recovery/truth.py::_add_field_stars()``, but this
module does not import anything from ``validation/`` - one-way
validation-to-production isolation, see
``validation/injection_recovery/tests/test_import_boundary.py``; the reverse
direction would be production depending on test code, which is
architecturally backwards. The synthetic-frame generator below is
reimplemented from scratch, not imported).

It proves:
  1. target_photometry.measure_target_flux() recovers a known aperture flux.
  2. driver._apply_quality() wires SP_RA/SP_DEC into FrameInfo.target_flux/
     target_snr when present, and leaves them None when absent (e.g. a
     calibration frame with no sky target).
  3. Under field-star contamination, the target-anchored photometric scale
     (combine.estimate_photometric_scales() with target_fluxes) stays close
     to 1 across frames of equal true brightness, while the old whole-image
     scale swings wildly on the exact same reprojected data - the same
     phenomenon the audit found in the real pipeline.
  4. summarize_stack_quality()'s new efficiency_target stays within the
     physical ~1.1 ceiling for a 4-frame stack under that same contamination,
     while the old whole-image efficiency (computed on the identical
     StackResult - same pixels, same frames) disagrees with it by a wide
     margin, demonstrating it remains untrustworthy even though production
     now assembles the stack correctly.
"""

from __future__ import annotations

import numpy as np
import pytest
from astropy.io import fits
from astropy.wcs import WCS
from saucepan_pipeline.driver import _apply_quality
from saucepan_pipeline.quality import assess_quality
from saucepan_pipeline.stacking.combine import (
    estimate_photometric_scales,
    stack_frames,
)
from saucepan_pipeline.stacking.metrics import compute_snr, summarize_stack_quality
from saucepan_pipeline.stacking.models import FrameInfo
from saucepan_pipeline.stacking.reproject import reproject_frame
from saucepan_pipeline.target_photometry import measure_target_flux

H = W = 96
RA_CENTER = 180.0
DEC_CENTER = 0.0
PIXEL_SCALE_ARCSEC = 1.0
TARGET_X, TARGET_Y = W / 2.0, H / 2.0
FIELD_STAR_EXCLUSION_PX = 35.0  # clear of the aperture (12) + annulus (18-26)


def _gaussian_star(h: int, w: int, fwhm_px: float, flux: float, x0: float, y0: float) -> np.ndarray:
    sigma = fwhm_px / 2.3548
    yy, xx = np.mgrid[0:h, 0:w]
    g = np.exp(-(((xx - x0) ** 2 + (yy - y0) ** 2) / (2 * sigma**2)))
    return g / g.sum() * flux


def _make_wcs() -> WCS:
    wcs = WCS(naxis=2)
    # CRPIX is 1-indexed per the FITS convention; +1 here makes the
    # reference sky position land exactly on 0-indexed pixel (TARGET_X,
    # TARGET_Y) - i.e. all_world2pix(..., 0) (0-indexed output) round-trips
    # exactly onto where _gaussian_star() places the target below.
    wcs.wcs.crpix = [W / 2.0 + 1, H / 2.0 + 1]
    wcs.wcs.crval = [RA_CENTER, DEC_CENTER]
    wcs.wcs.cdelt = [-PIXEL_SCALE_ARCSEC / 3600.0, PIXEL_SCALE_ARCSEC / 3600.0]
    wcs.wcs.ctype = ["RA---TAN", "DEC--TAN"]
    return wcs


def _field_star_position(rng: np.random.Generator) -> tuple[float, float]:
    """A position at least FIELD_STAR_EXCLUSION_PX from the target - far
    enough that field stars never leak into the target's own aperture or
    background annulus, so any contamination effect we see is purely a
    whole-image-heuristic problem, not a corrupted target measurement."""
    while True:
        fx = rng.uniform(8, W - 8)
        fy = rng.uniform(8, H - 8)
        if np.hypot(fx - TARGET_X, fy - TARGET_Y) >= FIELD_STAR_EXCLUSION_PX:
            return fx, fy


def _make_contaminated_frame(
    telescope_id: str,
    seed: int,
    *,
    target_flux: float = 3000.0,
    n_field_stars: int = 0,
    field_flux_lo: float = 0.0,
    field_flux_hi: float = 0.0,
    read_noise: float = 2.0,
    background: float = 100.0,
) -> FrameInfo:
    """Synthetic frame: one coherent target at (RA_CENTER, DEC_CENTER) plus
    ``n_field_stars`` independently-positioned-and-seeded field stars (not
    co-registered across frames, same as real fields from different
    pointings/epochs). Quality fields are populated the same way
    load_frame()/_apply_quality() would from real pixels."""
    rng = np.random.default_rng(seed)
    wcs = _make_wcs()

    data = rng.normal(background, read_noise, size=(H, W)).astype(np.float64)
    data += _gaussian_star(H, W, 4.0, target_flux, TARGET_X, TARGET_Y)
    for _ in range(n_field_stars):
        fx, fy = _field_star_position(rng)
        flux = rng.uniform(field_flux_lo, field_flux_hi)
        data += _gaussian_star(H, W, 4.0, flux, fx, fy)
    data = data.astype(np.float32)

    header = fits.Header()
    header.update(wcs.to_header())
    header["SP_RA"] = RA_CENTER
    header["SP_DEC"] = DEC_CENTER
    header["SP_TELE"] = telescope_id
    header["SP_EXPTIME"] = 30.0
    header["SP_PIXSCALE"] = PIXEL_SCALE_ARCSEC

    q = assess_quality(data)
    noise_adu = q["noise_adu"]
    bg_meas = q["background"]
    snr = compute_snr(data, bg_meas, noise_adu)

    frame = FrameInfo(
        path=f"/tmp/{telescope_id}.fits",
        telescope_id=telescope_id,
        data=data,
        header=header,
        wcs=wcs,
        noise_adu=noise_adu,
        background=bg_meas,
        snr=snr,
        fwhm_arcsec=4.0,
        pixel_scale_arcsec=PIXEL_SCALE_ARCSEC,
        exptime=30.0,
    )
    # Populate target_flux/target_snr the same way _apply_quality() does.
    return _apply_quality(frame)


# ── 1. target_photometry.py itself ──────────────────────────────────────


def test_measure_target_flux_recovers_known_flux() -> None:
    """Noiseless single Gaussian: the aperture (radius 12, ~7 sigma for
    FWHM=4) should capture essentially all of the injected flux, with the
    flat background exactly subtracted via the annulus median."""
    wcs = _make_wcs()
    flux_true = 5000.0
    data = np.full((H, W), 100.0) + _gaussian_star(H, W, 4.0, flux_true, TARGET_X, TARGET_Y)

    result = measure_target_flux(data, wcs, RA_CENTER, DEC_CENTER)

    assert result["ok"] is True
    assert result["flux"] == pytest.approx(flux_true, rel=1e-3)
    assert result["x"] == pytest.approx(TARGET_X, abs=0.5)
    assert result["y"] == pytest.approx(TARGET_Y, abs=0.5)


def test_measure_target_flux_off_frame_reports_not_ok() -> None:
    wcs = _make_wcs()
    data = np.full((H, W), 100.0)
    # A sky position far outside the frame's footprint.
    result = measure_target_flux(data, wcs, RA_CENTER + 5.0, DEC_CENTER)
    assert result["ok"] is False


# ── 2. driver._apply_quality() wiring ───────────────────────────────────


def test_apply_quality_populates_target_fields_when_ra_dec_present() -> None:
    frame = _make_contaminated_frame("tele-solo", seed=42, target_flux=3000.0)
    assert frame.target_flux is not None
    assert frame.target_flux == pytest.approx(3000.0, rel=0.2)
    assert frame.target_snr is not None
    assert frame.target_snr > 0
    # Existing whole-image fields must be untouched by the new code path.
    assert frame.snr > 0
    assert frame.noise_adu > 0


def test_apply_quality_leaves_target_fields_none_without_ra_dec() -> None:
    """A calibration frame (bias/dark/flat) has no real sky target - no
    SP_RA/SP_DEC on its header. This must not raise or reject the frame;
    the new fields simply stay None (the dataclass default)."""
    rng = np.random.default_rng(7)
    data = rng.normal(100.0, 2.0, size=(H, W)).astype(np.float32)
    header = fits.Header()
    header.update(_make_wcs().to_header())
    header["SP_TELE"] = "cal-frame"
    header["SP_EXPTIME"] = 1.0
    header["SP_PIXSCALE"] = PIXEL_SCALE_ARCSEC
    # Deliberately no SP_RA / SP_DEC.

    frame = FrameInfo(
        path="/tmp/cal.fits",
        telescope_id="cal-frame",
        data=data,
        header=header,
        wcs=_make_wcs(),
        pixel_scale_arcsec=PIXEL_SCALE_ARCSEC,
    )
    frame = _apply_quality(frame)

    assert frame.target_flux is None
    assert frame.target_snr is None
    # Whole-image quality measurement still ran normally.
    assert frame.noise_adu > 0


# ── 3 & 4. field-star-contaminated 4-frame stack ────────────────────────

# Deliberately asymmetric per-frame field-star populations - same "fixed
# fleet, differing per-epoch contamination" shape as the audited sweep.
# Field-star fluxes are chosen well above the target's own 3000 ADU so the
# whole-image 95th-percentile reference is dominated by them, not by the
# real (isolated, excluded-zone) target.
_CONTAMINATED_FLEET = [
    dict(telescope_id="tele-0", seed=1000, n_field_stars=2, field_flux_lo=1000, field_flux_hi=2000),
    dict(
        telescope_id="tele-1", seed=1001, n_field_stars=18, field_flux_lo=4000, field_flux_hi=9000
    ),
    dict(telescope_id="tele-2", seed=1002, n_field_stars=5, field_flux_lo=500, field_flux_hi=1500),
    dict(
        telescope_id="tele-3", seed=1003, n_field_stars=25, field_flux_lo=6000, field_flux_hi=12000
    ),
]


def _build_contaminated_fleet() -> list[FrameInfo]:
    return [_make_contaminated_frame(**cfg) for cfg in _CONTAMINATED_FLEET]


def test_photometric_scale_swings_less_with_target_anchoring() -> None:
    """Reproduces the audited finding directly: on the *same* reprojected
    arrays, the whole-image 95th-percentile scale (old, #410) swings far
    more across frames than the target-anchored scale (new, #471) - because
    the whole-image proxy is measuring field-star brightness (which differs
    hugely per frame by construction here), not the identical true target
    flux shared by every frame."""
    frames = _build_contaminated_fleet()
    ref_wcs, target_shape = frames[0].wcs, frames[0].data.shape

    reprojected = [reproject_frame(f, ref_wcs, target_shape)[0] for f in frames]

    old_scales = estimate_photometric_scales(reprojected)
    target_fluxes = [
        measure_target_flux(arr, ref_wcs, RA_CENTER, DEC_CENTER)["flux"] for arr in reprojected
    ]
    new_scales = estimate_photometric_scales(reprojected, target_fluxes)

    old_spread = max(old_scales) / min(old_scales)
    new_spread = max(new_scales) / min(new_scales)

    # Audited swing was 0.489-1.584 (ratio ~3.24). This synthetic fleet
    # reproduces the same shape of failure, not the exact historical ratio.
    assert old_spread > 2.0, f"expected old whole-image scale to swing widely, got {old_scales}"
    assert new_spread < 1.15, f"expected target-anchored scale near 1, got {new_scales}"


def test_efficiency_target_stays_within_ceiling_under_field_star_contamination() -> None:
    """The core regression: efficiency_target must stay within the physical
    stacking ceiling (sqrt(N) is the theoretical max SNR gain, so efficiency
    <= ~1 is physically expected, with a small margin for measurement noise)
    even with heavy, asymmetric field-star contamination present - while the
    old whole-image efficiency, computed from the identical StackResult,
    disagrees with it by a wide margin (it's measuring generic bright-pixel
    brightness, dominated by the numerous field stars, not the one true
    target - see module docstring)."""
    frames = _build_contaminated_fleet()

    result = stack_frames(
        frames,
        photometric_scale=True,
        sigma_clip=3.0,
        auto_crop=True,
        weight_by_fwhm=True,
    )
    m = summarize_stack_quality(result, frames)

    assert m["efficiency_target"] is not None
    assert 0.0 < m["efficiency_target"] <= 1.1

    # Documentation/contrast: the pre-existing whole-image efficiency field
    # is untouched by #471 and still reflects the same blind spot #468
    # root-caused in the real pipeline - it disagrees substantially with
    # the trustworthy target-anchored number on this exact stacked input.
    assert m["efficiency"] is not None
    relative_gap = abs(m["efficiency"] - m["efficiency_target"]) / m["efficiency_target"]
    assert relative_gap > 0.3, (
        f"expected old/new efficiency to diverge under contamination, "
        f"got efficiency={m['efficiency']} efficiency_target={m['efficiency_target']}"
    )
