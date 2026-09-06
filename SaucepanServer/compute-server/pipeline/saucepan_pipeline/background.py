"""
Background subtraction module for Saucepan pipeline.

Handles 2D background estimation and subtraction for astronomical images.
"""

import logging

import numpy as np

logger = logging.getLogger(__name__)


def subtract_background(
    data: np.ndarray, box_size: int = 50, filter_size: int = 3
) -> tuple[np.ndarray, np.ndarray]:
    """
    Subtract 2D background from image data using photutils Background2D.

    Args:
        data: 2D float32 image array (must be in electrons or ADU — NOT normalized to [0,1])
        box_size: Size of background estimation boxes in pixels (default 50)
        filter_size: Median filter size for background smoothing (default 3)

    Returns:
        (background_subtracted_data, background_map)
        background_subtracted_data: data with background removed
        background_map: the estimated 2D background (for diagnostics/headers)
    """
    if data.ndim != 2:
        raise ValueError(f"Expected 2D array, got {data.ndim}D array")

    if data.dtype != np.float32:
        logger.warning(f"Input data type is {data.dtype}, converting to float32")
        data = data.astype(np.float32)

    # Clamp box_size to reasonable limits
    min_size = min(data.shape)
    if box_size > min_size // 2:
        clamped_box_size = max(1, min_size // 4)
        logger.warning(
            f"box_size ({box_size}) exceeds safe limit for image shape {data.shape}, "
            f"clamping to {clamped_box_size}"
        )
        box_size = clamped_box_size

    try:
        # Try to use photutils for robust 2D background estimation
        from astropy.stats import SigmaClip
        from photutils.background import Background2D, MedianBackground

        logger.info(
            f"Using photutils Background2D with box_size={box_size}, filter_size={filter_size}"
        )

        # Create background estimator
        sigma_clip = SigmaClip(sigma=3)
        bkg_estimator = MedianBackground()

        # Estimate 2D background
        bkg = Background2D(
            data,
            (box_size, box_size),
            filter_size=(filter_size, filter_size),
            sigma_clip=sigma_clip,
            bkg_estimator=bkg_estimator,
        )

        background_map = bkg.background

        # Log statistics
        median_bkg = np.median(background_map)
        logger.info(f"Median background level: {median_bkg:.1f} ADU")

    except ImportError:
        logger.warning(
            "photutils not available, falling back to global scalar background subtraction"
        )

        # Fallback: subtract global median background
        median_bkg = np.median(data)
        background_map = np.full_like(data, median_bkg, dtype=np.float32)

        logger.info(f"Using global median background: {median_bkg:.1f} ADU")

    # Subtract background
    background_subtracted = data - background_map

    # Clip to physical floor (no negative pixels)
    background_subtracted = np.clip(background_subtracted, 0, None)

    # Ensure output is float32
    background_subtracted = background_subtracted.astype(np.float32)
    background_map = background_map.astype(np.float32)

    return background_subtracted, background_map


def get_background_rms(data: np.ndarray, box_size: int = 50) -> float:
    """
    Estimate per-frame background RMS for use as stacking weight denominator (1/rms²).

    Args:
        data: 2D float32 image array
        box_size: Size of background estimation boxes in pixels (default 50)

    Returns:
        Scalar RMS of the background noise
    """
    if data.ndim != 2:
        raise ValueError(f"Expected 2D array, got {data.ndim}D array")

    try:
        # Try to use photutils for RMS estimation
        from astropy.stats import SigmaClip
        from photutils.background import Background2D, MedianBackground

        # Clamp box_size
        min_size = min(data.shape)
        if box_size > min_size // 2:
            box_size = max(1, min_size // 4)

        sigma_clip = SigmaClip(sigma=3)
        bkg_estimator = MedianBackground()

        # Estimate 2D background to get RMS map
        bkg = Background2D(
            data,
            (box_size, box_size),
            filter_size=(3, 3),
            sigma_clip=sigma_clip,
            bkg_estimator=bkg_estimator,
        )

        # Use median of RMS map
        rms_map = bkg.background_rms
        background_rms = float(np.median(rms_map))

        logger.debug(f"Background RMS (photutils): {background_rms:.1f}")

        return background_rms

    except ImportError:
        logger.debug("photutils not available, using std() as RMS fallback")

        # Fallback: use standard deviation of data
        background_rms = float(np.std(data))

        logger.debug(f"Background RMS (std fallback): {background_rms:.1f}")

        return background_rms
