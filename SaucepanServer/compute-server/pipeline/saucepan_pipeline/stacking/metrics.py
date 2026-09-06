"""Background noise, SNR, and pixel-scale measurement for stacking."""

from typing import TYPE_CHECKING

import numpy as np
from astropy.io import fits

from saucepan_pipeline.quality import assess_quality
from saucepan_pipeline.target_photometry import measure_target_flux

if TYPE_CHECKING:
    from saucepan_pipeline.stacking.models import FrameInfo, StackResult


def measure_background_noise(data: np.ndarray) -> tuple:
    """
    Measure background level and noise on background-only pixels.

    Delegates to quality.assess_quality() rather than reimplementing the
    star-masked noise estimate here - this used to be an independent copy
    of that logic and had drifted back into the exact floor-censoring bug
    quality.py was fixed for (#464: background.py's np.clip(x, 0, None)
    ties ~half of any background-dominated image to an identical value,
    which collapses a plain MAD/median call; see quality._robust_sigma()).

    Returns:
        (background_median, noise_adu, star_pixel_count, saturated_count)
    """
    q = assess_quality(data)
    return q["background"], q["noise_adu"], q["star_pixels"], q["saturated_pixels"]


def compute_snr(data: np.ndarray, bg: float, noise: float) -> float:
    """Compute SNR as (99.5th_percentile - bg) / noise on non-saturated pixels."""
    flat = data.ravel()
    nonsat = flat[flat < 65500]
    if len(nonsat) > 1000:
        p995 = np.percentile(nonsat, 99.5)
    else:
        p995 = bg
    signal = p995 - bg
    return float(signal / noise) if noise > 0 else 0.0


def get_pixel_scale_from_header(header: fits.Header) -> float | None:
    """Extract pixel scale in arcsec/pixel from FITS header."""
    for key in ["SP_PIXSCALE", "PIXSCALE"]:
        if key in header:
            return float(header[key])
    if "CDELT2" in header:
        cdelt = header["CDELT2"]
        if cdelt != 0:
            return abs(float(cdelt)) * 3600.0
    if "CD2_2" in header:
        cd = header["CD2_2"]
        if cd != 0:
            return abs(float(cd)) * 3600.0
    return None


def measure_image_quality(data: np.ndarray) -> dict:
    """
    Measure background noise and SNR, masking star pixels first.

    Delegates to quality.assess_quality() - see measure_background_noise()
    above for why this must not be a second, independent implementation.

    Returns:
        dict with background, noise_adu, snr, star_pixels
    """
    return assess_quality(data)


def summarize_stack_quality(result: "StackResult", frames: "list[FrameInfo]") -> dict:
    """
    Stack SNR/noise/gain/efficiency for a completed StackResult.

    Single source of truth for this metric - build_output_header() (FITS
    SP_ headers) and run_stack_pipeline()'s return dict both call this
    instead of each re-deriving it, which is exactly the pattern that let
    the #464 floor-censoring bug survive in one copy after being fixed in
    another (see measure_background_noise() above).

    Noise is read from result.noise_map - the analytic inverse-variance
    propagation from each contributing frame's own (correctly-measured,
    native-pixel-space) noise_adu - never re-measured from the resampled
    stack's pixels. reproject_interp() interpolates onto the common output
    grid, which correlates noise across neighboring pixels; a naive
    MAD-based re-measurement on that resampled image systematically
    understates the noise and so overstates SNR (confirmed by isolation
    test: pure noise, no stars, upsampling 1.2->0.9"/px alone cut measured
    noise ~35% and fabricated a nonzero star_pixels count out of nothing).
    Signal still comes from assess_quality()'s pixel-based measurement -
    only the noise side of the ratio is affected by resampling correlation.
    """
    flat = result.science[~np.isnan(result.science)]
    pix_q = (
        assess_quality(flat)
        if len(flat) > 0
        else {"background": 0.0, "signal_adu": 0.0, "star_pixels": 0}
    )

    valid_noise = result.noise_map[np.isfinite(result.noise_map)]
    stack_noise_adu = float(np.median(valid_noise)) if valid_noise.size > 0 else 0.0
    stack_snr = pix_q["signal_adu"] / stack_noise_adu if stack_noise_adu > 0 else 0.0

    best_single_snr = max((f.snr for f in frames if f.snr > 0), default=0.0)
    theoretical_max = float(np.sqrt(max(result.n_frames, 1)))
    snr_gain = stack_snr / best_single_snr if best_single_snr > 0 else 0.0
    efficiency = snr_gain / theoretical_max if theoretical_max > 0 else 0.0

    # Position-anchored equivalents (#471) - same formulas as above, but
    # sourced from aperture flux/SNR measured at the science target's own
    # sky position rather than the whole-image proxies, so they're immune
    # to the field-star / cosmic-ray contamination the fields above are
    # vulnerable to. None (not 0.0) whenever no frame carries a resolvable
    # SP_RA/SP_DEC (e.g. a calibration-only stack) - that distinguishes
    # "not measurable" from "measured as zero".
    stack_target_flux: float | None = None
    stack_snr_target: float | None = None
    best_single_snr_target: float | None = None
    snr_gain_target: float | None = None
    efficiency_target: float | None = None

    ra: float | None = None
    dec: float | None = None
    for f in frames:
        raw_ra, raw_dec = f.header.get("SP_RA"), f.header.get("SP_DEC")
        if raw_ra is None or raw_dec is None:
            continue
        try:
            ra, dec = float(raw_ra), float(raw_dec)
        except (TypeError, ValueError):
            continue
        break

    if ra is not None and dec is not None and len(flat) > 0:
        tgt = measure_target_flux(result.science, result.ref_wcs, ra, dec)
        if tgt.get("ok"):
            stack_target_flux = float(tgt["flux"])
            if stack_noise_adu > 0:
                stack_snr_target = stack_target_flux / stack_noise_adu

        single_target_snrs = [f.target_snr for f in frames if f.target_snr]
        if single_target_snrs:
            best_single_snr_target = max(single_target_snrs)

        if (
            stack_snr_target is not None
            and best_single_snr_target is not None
            and best_single_snr_target > 0
        ):
            snr_gain_target = stack_snr_target / best_single_snr_target
            if theoretical_max > 0:
                efficiency_target = snr_gain_target / theoretical_max

    return {
        "background": pix_q["background"],
        "stack_noise_adu": round(stack_noise_adu, 4),
        "stack_snr": round(stack_snr, 1),
        "star_pixels": pix_q["star_pixels"],
        "best_single_snr": round(best_single_snr, 1),
        "snr_gain": round(snr_gain, 2),
        "theoretical_max": round(theoretical_max, 2),
        "efficiency": round(efficiency, 3),
        "stack_target_flux": (
            round(stack_target_flux, 4) if stack_target_flux is not None else None
        ),
        "stack_snr_target": (round(stack_snr_target, 1) if stack_snr_target is not None else None),
        "best_single_snr_target": (
            round(best_single_snr_target, 1) if best_single_snr_target is not None else None
        ),
        "snr_gain_target": (round(snr_gain_target, 2) if snr_gain_target is not None else None),
        "efficiency_target": (
            round(efficiency_target, 3) if efficiency_target is not None else None
        ),
    }
