"""Core datatypes for heterogeneous image stacking."""

from dataclasses import dataclass, field

import numpy as np
from astropy.io import fits
from astropy.wcs import WCS


@dataclass
class FrameInfo:
    """Metadata about a single frame for stacking."""

    path: str
    telescope_id: str
    data: np.ndarray = field(repr=False)
    header: fits.Header = field(repr=False)
    wcs: WCS = field(repr=False)
    noise_adu: float = 0.0
    background: float = 0.0
    snr: float = 0.0
    fwhm_arcsec: float = 0.0
    pixel_scale_arcsec: float = 0.0
    exptime: float = 0.0
    weight: float = 0.0
    saturated_pixels: int = 0
    sp_emulator: bool = False
    # Per-pixel exclusion mask in the frame's *native* pixel grid (#413).
    # bool, True = drop this pixel from the stack. Populated by load_frame()
    # from saturation + an optional bad-pixel map, then OR-extended with the
    # cosmic-ray mask in driver._calibrate_in_memory(). None = nothing masked.
    # reproject.reproject_mask() carries it onto the common grid; combine
    # zeroes the weight cube there before the #411 per-pixel sigma-clip.
    mask: np.ndarray | None = field(default=None, repr=False)
    # Human-readable list of which stages contributed to ``mask`` (e.g.
    # ["saturation", "bad_pixel_map", "cosmic_ray"]) - copied into provenance.
    mask_sources: list[str] = field(default_factory=list)
    # Position-anchored aperture flux/SNR at the frame's own SP_RA/SP_DEC
    # (#471), measured in native pixel space by target_photometry.py. None
    # whenever the frame has no resolvable sky target (e.g. bias/dark/flat
    # calibration frames) or the aperture measurement fails - not an error,
    # the expected path for those frames. Additive alongside the existing
    # whole-image ``snr``/``noise_adu`` above; those are untouched.
    target_flux: float | None = None
    target_snr: float | None = None


@dataclass
class StackResult:
    """Result of a stacking operation."""

    science: np.ndarray
    weight_map: np.ndarray
    noise_map: np.ndarray
    coverage_map: np.ndarray  # number of frames contributing per pixel
    ref_wcs: WCS
    n_frames: int
    n_rejected: int
    provenance: list[dict]
    crop_slice: tuple | None = None  # (y1, y2, x1, x2) if auto-cropped
