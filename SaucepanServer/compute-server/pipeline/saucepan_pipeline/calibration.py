"""
Image calibration module for Saucepan pipeline.

Handles dark, flat, and bias frame calibration of astronomical images, gain
conversion to electrons, and opt-in cosmic-ray rejection (L.A.Cosmic via
astroscrappy, mask-producing — see ``remove_cosmic_rays``).
"""

import logging
from collections.abc import Mapping
from pathlib import Path
from typing import Any

import numpy as np
from astropy.io import fits

logger = logging.getLogger(__name__)

try:
    import astroscrappy

    _HAVE_ASTROSCRAPPY = True
except ImportError:  # pragma: no cover - exercised only where the dep is absent
    astroscrappy = None
    _HAVE_ASTROSCRAPPY = False

# Documented fallbacks for the L.A.Cosmic noise model when the frame's headers
# don't carry the values. 16-bit full-well for satlevel; a mid-range CMOS read
# noise; unity gain (data already electron-scaled by the gain step, see below).
_CR_GAIN_DEFAULT = 1.0  # e-/ADU
_CR_READNOISE_DEFAULT = 6.5  # e-
_CR_SATLEVEL_DEFAULT = 65535.0  # counts


def calibrate_image(
    input_path: str,
    output_path: str,
    filter_name: str,
    dark_path: str | None = None,
    flat_path: str | None = None,
    bias_path: str | None = None,
    calstat: str | None = None,
    config: dict[str, Any] | None = None,
) -> str:
    """
    Calibrate an astronomical image using dark, flat, and bias frames.

    This is an orchestrator function that delegates to smaller, focused functions.

    Args:
        input_path: Path to input FITS file
        output_path: Path for output calibrated FITS file
        filter_name: Filter used for acquisition (for flat selection)
        dark_path: Optional path to dark frame
        flat_path: Optional path to flat frame
        bias_path: Optional path to bias frame
        calstat: Optional SP_CALSTAT header value (NONE, B, D, F, BDF, etc.)
        config: Optional configuration dictionary

    Returns:
        Path to calibrated output file
    """
    config = config or {}
    calstat = calstat or "NONE"

    logger.info(f"Calibrating image: {input_path}")
    logger.info(f"Filter: {filter_name}")
    logger.info(f"SP_CALSTAT (input): {calstat}")

    try:
        data, header = load_and_prepare_image(input_path)

        calibrated_data, applied_steps, apply_dark, apply_flat, apply_bias = (
            apply_calibration_steps(data, calstat, bias_path, dark_path, flat_path)
        )

        calibrated_data, header = apply_gain_conversion(calibrated_data, header, input_path, config)

        # remove_cosmic_rays defaults OFF (opt-in). Rationale: the real
        # stacking path (driver._calibrate_in_memory -> POST /v1/stack)
        # already defaults it off and the strict pipeline-order contract does
        # not mandate a CR stage. The rejector is now L.A.Cosmic
        # (astroscrappy), which spares PSF-shaped sources, but the default is
        # left opt-in for parity with the stack path. Callers that want it
        # pass config={"remove_cosmic_rays": True}. Keep this default in sync
        # with driver._calibrate_in_memory. The CR mask is discarded on this
        # write-to-disk path; the in-memory stack driver instead keeps it and
        # OR-s it into FrameInfo.mask so stacking drops those pixels to
        # weight 0 rather than trusting the fill value (#413 hybrid policy).
        if config.get("remove_cosmic_rays", False):
            calibrated_data, _cr_mask = remove_cosmic_rays(calibrated_data, header=header)

        calibrated_data = apply_normalization(calibrated_data, header, config)

        new_calstat = update_calstat(calstat, applied_steps)
        update_header_with_calibration(
            header, filter_name, apply_dark, apply_flat, apply_bias, new_calstat
        )

        save_calibrated_image(calibrated_data, header, output_path)

        return output_path

    except Exception as e:
        logger.error(f"Calibration failed: {e}")
        raise


def load_and_prepare_image(input_path: str) -> tuple[np.ndarray, dict]:
    """Load FITS image and return data and header."""
    with fits.open(input_path) as hdul:
        data = hdul[0].data.astype(np.float32)
        header = hdul[0].header.copy()
    return data, header


def determine_calibration_steps(
    calstat: str, bias_path: str | None, dark_path: str | None, flat_path: str | None
) -> tuple[bool, bool, bool]:
    """Determine which calibration steps to apply based on calstat and frame existence."""
    apply_bias = bool(bias_path and Path(bias_path).exists() and "B" not in calstat)
    apply_dark = bool(dark_path and Path(dark_path).exists() and "D" not in calstat)
    apply_flat = bool(flat_path and Path(flat_path).exists() and "F" not in calstat)
    return apply_bias, apply_dark, apply_flat


def apply_calibration_steps(
    data: np.ndarray,
    calstat: str,
    bias_path: str | None,
    dark_path: str | None,
    flat_path: str | None,
) -> tuple[np.ndarray, str, bool, bool, bool]:
    """Apply bias, dark, and flat calibration steps."""
    calibrated_data = data.copy()
    applied_steps = ""

    apply_bias, apply_dark, apply_flat = determine_calibration_steps(
        calstat, bias_path, dark_path, flat_path
    )

    if apply_bias and bias_path:
        logger.info("Applying bias correction")
        bias_data = load_calibration_frame(bias_path)
        if bias_data.shape == calibrated_data.shape:
            calibrated_data = calibrated_data - bias_data
            applied_steps += "B"
        else:
            logger.warning(
                f"Bias frame shape mismatch: {bias_data.shape} != {calibrated_data.shape}"
            )
    elif "B" in calstat:
        logger.info("Bias already applied (B in SP_CALSTAT), skipping")

    if apply_dark and dark_path:
        logger.info("Applying dark correction")
        dark_data = load_calibration_frame(dark_path)
        if dark_data.shape == calibrated_data.shape:
            calibrated_data = calibrated_data - dark_data
            applied_steps += "D"
        else:
            logger.warning(
                f"Dark frame shape mismatch: {dark_data.shape} != {calibrated_data.shape}"
            )
    elif "D" in calstat:
        logger.info("Dark already applied (D in SP_CALSTAT), skipping")

    if apply_flat and flat_path:
        logger.info("Applying flat field correction")
        flat_data = load_calibration_frame(flat_path)
        if flat_data.shape == calibrated_data.shape:
            flat_norm = flat_data / np.median(flat_data)
            flat_norm[flat_norm == 0] = 1.0
            calibrated_data = calibrated_data / flat_norm
            applied_steps += "F"
        else:
            logger.warning(
                f"Flat frame shape mismatch: {flat_data.shape} != {calibrated_data.shape}"
            )
    elif "F" in calstat:
        logger.info("Flat already applied (F in SP_CALSTAT), skipping")

    return calibrated_data, applied_steps, apply_dark, apply_flat, apply_bias


def apply_gain_conversion(
    data: np.ndarray, header: dict, input_path: str, config: dict[str, Any]
) -> tuple[np.ndarray, dict]:
    """Apply gain conversion from ADU to electrons."""
    gain = config.get("gain")

    if gain is None:
        # The stacking driver has already loaded this header in
        # stacking.frames.load_frame(). Reuse it so calibration does not
        # reopen the science FITS just to recover gain metadata. Keep the
        # file fallback for callers that do not provide the metadata.
        gain = header.get("SP_GAIN") or header.get("GAIN")

    if gain is None:
        try:
            with fits.open(input_path) as hdul:
                gain = hdul[0].header.get("SP_GAIN") or hdul[0].header.get("GAIN")
        except Exception:
            pass

    if gain and float(gain) > 0:
        data = data * float(gain)
        header["SP_BUNIT"] = "electron"
        logger.info(f"Applied gain conversion: {gain} e-/ADU")
    else:
        header["SP_BUNIT"] = "adu"
        logger.warning("No gain available — data remains in ADU")

    return data, header


def apply_normalization(data: np.ndarray, header: dict, config: dict[str, Any]) -> np.ndarray:
    """Apply normalization if requested and appropriate."""
    should_normalize = config.get("normalize", False)

    if should_normalize and header.get("SP_BUNIT") == "electron":
        logger.warning("normalize=True ignored — data is in electrons")
        return data
    elif should_normalize:
        logger.info("Normalizing image")
        return normalize_image(data)

    return data


def update_calstat(current_calstat: str, applied_steps: str) -> str:
    """Merge applied calibration steps with existing calstat."""
    if not applied_steps:
        return current_calstat

    new_calstat_set = set(current_calstat.replace("NONE", "")) | set(applied_steps)
    new_calstat = "".join(sorted(new_calstat_set)) if new_calstat_set else "NONE"
    logger.info(f"Updated SP_CALSTAT: {current_calstat} -> {new_calstat}")
    return new_calstat


def update_header_with_calibration(
    header: dict,
    filter_name: str,
    apply_dark: bool,
    apply_flat: bool,
    apply_bias: bool,
    new_calstat: str,
) -> None:
    """Update FITS header with calibration metadata."""
    header["CALIBRAT"] = (True, "Image calibrated")
    header["CAL_FILT"] = (filter_name, "Filter used for calibration")
    header["CAL_DARK"] = (apply_dark, "Dark frame applied")
    header["CAL_FLAT"] = (apply_flat, "Flat frame applied")
    header["CAL_BIAS"] = (apply_bias, "Bias frame applied")
    header["SP_CALSTAT"] = (new_calstat, "Calibrations applied: B=bias D=dark F=flat or NONE")
    header["HISTORY"] = "Calibrated by Saucepan pipeline"


def save_calibrated_image(data: np.ndarray, header: dict, output_path: str) -> None:
    """Save calibrated image to FITS file."""
    hdu = fits.PrimaryHDU(data, header)
    hdu.writeto(output_path, overwrite=True)
    logger.info(f"Calibrated image saved to: {output_path}")


def load_calibration_frame(frame_path: str) -> np.ndarray:
    """Load a calibration frame (dark, flat, or bias)."""
    with fits.open(frame_path) as hdul:
        data = hdul[0].data.astype(np.float32)

    # Handle NaN and Inf values
    data = np.nan_to_num(data, nan=0.0, posinf=0.0, neginf=0.0)
    return data


def _header_float(header: Mapping[str, Any], key: str, default: float) -> float:
    """Read a positive float from a FITS header, else return ``default``."""
    try:
        value = float(header.get(key))  # type: ignore[arg-type]
    except (TypeError, ValueError):
        return default
    return value if value > 0 else default


def _remove_cosmic_rays_mad(
    data: np.ndarray, sigma: float
) -> tuple[np.ndarray, np.ndarray]:
    """Legacy global-MAD cosmic-ray clip (no spatial awareness).

    Retained only as the fallback when astroscrappy is unavailable. Pixels
    above ``median + sigma * MAD`` are replaced with the median in a copy.
    """
    median = np.median(data)
    mad = np.median(np.abs(data - median))
    threshold = median + sigma * mad

    cleaned = data.copy()
    mask = data > threshold
    if np.any(mask):
        cleaned[mask] = median
        logger.info("Removed %d cosmic ray pixels (global-MAD fallback)", int(mask.sum()))
    return cleaned, mask


def remove_cosmic_rays(
    data: np.ndarray,
    sigma: float = 5.0,
    *,
    header: Mapping[str, Any] | None = None,
) -> tuple[np.ndarray, np.ndarray]:
    """
    Cosmic-ray rejection via L.A.Cosmic (van Dokkum 2001), using astroscrappy.

    Pure: the caller's array is never mutated. ``astroscrappy.detect_cosmics``
    is a Laplacian edge detector — it separates sharp single-/few-pixel CR
    hits from PSF-shaped sources, so genuine stellar cores are left intact
    (unlike the old global-MAD clip this replaced, issue #441).

    ``gain`` (e-/ADU), ``readnoise`` (e-) and ``satlevel`` feed only the
    internal noise model. They are read from the ``SP_GAIN`` / ``SP_RDNOISE``
    / ``SP_SATURATE`` headers when ``header`` is supplied, otherwise the
    documented defaults (1.0, 6.5, 65535.0) apply. When ``SP_BUNIT`` is
    ``electron`` the pixels are already electron-scaled by the gain step, so
    ``gain`` is pinned to 1.0 for the noise model.

    If astroscrappy cannot be imported, falls back to the legacy global-MAD
    clip and logs a warning (the dependency is declared, so this is
    belt-and-braces only).

    Args:
        data: Input image data, still background-inclusive (not modified).
        sigma: Laplacian-to-noise detection limit (astroscrappy ``sigclip``).
        header: Optional FITS header for gain/readnoise/satlevel lookup.

    Returns:
        ``(cleaned, mask)`` — ``cleaned`` is a new array (a copy of ``data``
        when nothing is flagged); ``mask`` is a boolean array, True where a
        cosmic ray was detected and replaced. Issue #413 consumes the mask.
    """
    if data.size == 0:
        return data, np.zeros(data.shape, dtype=bool)

    if not _HAVE_ASTROSCRAPPY:
        logger.warning(
            "astroscrappy unavailable — falling back to legacy global-MAD "
            "cosmic-ray clip (no spatial awareness)"
        )
        return _remove_cosmic_rays_mad(data, sigma)

    hdr: Mapping[str, Any] = header or {}
    gain = _header_float(hdr, "SP_GAIN", _CR_GAIN_DEFAULT)
    readnoise = _header_float(hdr, "SP_RDNOISE", _CR_READNOISE_DEFAULT)
    satlevel = _header_float(hdr, "SP_SATURATE", _CR_SATLEVEL_DEFAULT)
    if str(hdr.get("SP_BUNIT", "")).lower() == "electron":
        gain = 1.0

    mask, cleaned = astroscrappy.detect_cosmics(
        np.ascontiguousarray(data, dtype=np.float32),
        sigclip=sigma,
        gain=gain,
        readnoise=readnoise,
        satlevel=satlevel,
    )
    mask = np.asarray(mask, dtype=bool)
    if mask.any():
        logger.info("L.A.Cosmic flagged %d cosmic-ray pixels", int(mask.sum()))
    return cleaned.astype(data.dtype, copy=False), mask


def normalize_image(data: np.ndarray) -> np.ndarray:
    """
    Normalize image data to 0-1 range.

    Args:
        data: Input image data

    Returns:
        Normalized image data
    """
    if data.size == 0:
        return data

    # Remove outliers (top/bottom 1%)
    flat_data = data.flatten()
    sorted_data = np.sort(flat_data)
    min_percentile = sorted_data[int(0.01 * len(sorted_data))]
    max_percentile = sorted_data[int(0.99 * len(sorted_data))]

    # Clip and normalize
    clipped = np.clip(data, min_percentile, max_percentile)
    normalized = (clipped - min_percentile) / (max_percentile - min_percentile + 1e-10)

    return normalized
