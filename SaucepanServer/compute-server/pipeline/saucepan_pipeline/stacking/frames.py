"""Load FITS frames and attach quality metrics for stacking.

Trust model (#292):
  SP_* quality headers (SP_BGMD, SP_BGNOI, SP_SNR, SP_FWHM, SP_SPX, …) are
  write-once by the compute quality/assess stage — never authoritative when
  read back from an untrusted upload. ``load_frame()`` always re-measures
  background/noise/SNR/FWHM from pixel data. Client-supplied SP_FWHM / SEEING
  are ignored.
"""


from pathlib import Path

import numpy as np
from astropy.io import fits
from astropy.wcs import WCS
from grading.fits_limits import ensure_fits_loadable

from saucepan_pipeline.quality import estimate_fwhm
from saucepan_pipeline.stacking.metrics import (
    compute_snr,
    get_pixel_scale_from_header,
    measure_background_noise,
)
from saucepan_pipeline.stacking.models import FrameInfo


# Fallback full-well when the frame carries no SP_SATURATE header. Matches
# calibration._CR_SATLEVEL_DEFAULT (16-bit sensor full-scale).
_SATURATE_DEFAULT = 65535.0


def _load_bad_pixel_map(path: str, expected_shape: tuple, frame_path: str):
    """Load a bad-pixel map from ``SP_BPMASK`` (FITS or ``.npy``).

    ``path`` may be absolute or relative to the frame's own directory.
    Returns a bool array (True = bad) of ``expected_shape``, or None if the
    file is missing / unreadable / the wrong shape - a missing map is the
    normal case, not an error.
    """
    if not path:
        return None
    frame_dir = Path(frame_path).resolve().parent
    candidate = Path(path)
    if not candidate.is_absolute():
        candidate = frame_dir / candidate
    candidate = candidate.resolve()
    try:
        candidate.relative_to(frame_dir)
    except ValueError:
        # A header is untrusted input.  A bad-pixel map may be a sibling of
        # the frame, never an arbitrary path elsewhere on the host.
        return None
    if not candidate.is_file():
        return None
    try:
        if candidate.suffix.lower() == ".npy":
            # mmap + allow_pickle=False prevents a header-selected .npy from
            # materializing an arbitrary object array before shape validation.
            bpm = np.load(candidate, mmap_mode="r", allow_pickle=False)
            if not isinstance(bpm, np.ndarray) or bpm.shape != expected_shape:
                return None
            return np.asarray(bpm, dtype=bool).copy()
        else:
            with fits.open(candidate, memmap=True) as hdul:
                ensure_fits_loadable(str(candidate), hdul[0].header)
                bpm = hdul[0].data
    except (OSError, ValueError):
        return None
    if bpm is None or bpm.shape != expected_shape:
        return None
    return np.asarray(bpm, dtype=bool).copy()


def _build_native_mask(data: np.ndarray, header, frame_path: str) -> tuple:
    """Per-pixel exclusion mask in native pixel space (#413).

    Sources folded in here (CR mask is added later in the driver):
      * saturation: ``data >= SP_SATURATE`` (or the 65535 fallback).
      * bad-pixel map: ``SP_BPMASK`` sidecar (FITS/npy); absent -> skipped.

    Returns ``(mask_or_None, sources)`` where ``sources`` is a list of the
    contributing stage names. ``None`` when nothing is masked.
    """
    sources: list[str] = []
    mask = np.zeros(data.shape, dtype=bool)

    try:
        sat_level = float(header.get("SP_SATURATE") or _SATURATE_DEFAULT)
    except (TypeError, ValueError):
        sat_level = _SATURATE_DEFAULT
    if sat_level > 0:
        sat_mask = data >= sat_level
        if sat_mask.any():
            mask |= sat_mask
            sources.append("saturation")

    # TODO(satellite-trails): a cheap Hough / astride-style linear-streak
    # detector would add a "satellite_trail" source here. Deferred from #413
    # (does not fit in one file without a new transform dependency) - file a
    # follow-up issue before implementing.
    bpm = _load_bad_pixel_map(
        str(header.get("SP_BPMASK") or ""), data.shape, frame_path
    )
    if bpm is not None and bpm.any():
        mask |= bpm
        sources.append("bad_pixel_map")

    if not mask.any():
        return None, []
    return mask, sources


def _header_bool(hdr, key: str) -> bool:
    if key not in hdr:
        return False
    try:
        return bool(int(hdr[key]))
    except (TypeError, ValueError):
        val = str(hdr[key]).strip().lower()
        return val in {"1", "true", "t", "yes"}


def load_frame(path: str, telescope_id: str | None = None) -> FrameInfo:
    """
    Load a FITS file and measure its quality from pixels.

    Ignores untrusted SP_* quality headers on the input (forged values must
    not affect inverse-variance / FWHM weights). See module trust model.

    Args:
        path: Path to FITS file
        telescope_id: Optional override. If None, read from header.

    Returns:
        FrameInfo with data, WCS, noise, SNR, measured FWHM, etc.
    """
    with fits.open(path) as hdul:
        header = hdul[0].header
        ensure_fits_loadable(path, header)
        data = hdul[0].data.astype(np.float32)

    try:
        wcs = WCS(header)
        if not wcs.has_celestial:
            raise ValueError("No celestial axes")
    except Exception:
        ra = header.get("SP_RA") or header.get("CRVAL1") or header.get("RA", 0.0)
        dec = header.get("SP_DEC") or header.get("CRVAL2") or header.get("DEC", 0.0)
        ps = get_pixel_scale_from_header(header) or 1.0
        h, w = data.shape
        wcs_header = fits.Header()
        wcs_header["NAXIS"] = 2
        wcs_header["NAXIS1"] = w
        wcs_header["NAXIS2"] = h
        wcs_header["CTYPE1"] = "RA---TAN"
        wcs_header["CTYPE2"] = "DEC--TAN"
        wcs_header["CRPIX1"] = w / 2.0
        wcs_header["CRPIX2"] = h / 2.0
        wcs_header["CRVAL1"] = float(ra)
        wcs_header["CRVAL2"] = float(dec)
        wcs_header["CDELT1"] = -ps / 3600.0
        wcs_header["CDELT2"] = ps / 3600.0
        wcs = WCS(wcs_header)

    # Always re-measure — never short-circuit on SP_BGMD/SP_BGNOI/SP_SNR/SP_FWHM (#292).
    bg, noise, _star_px, sat = measure_background_noise(data)
    snr = compute_snr(data, bg, noise)
    pixel_scale = get_pixel_scale_from_header(header)
    ps = float(pixel_scale) if pixel_scale else 1.0
    # Measure FWHM in-process; ignore client SP_FWHM / SEEING (#409).
    fwhm = estimate_fwhm(data, pixel_scale_arcsec=ps)
    exptime = header.get("SP_EXPTIME") or header.get("EXPTIME") or 1.0
    tid = telescope_id or header.get("SP_TELE", "unknown")
    sp_emulator = _header_bool(header, "SP_EMULATOR")
    native_mask, mask_sources = _build_native_mask(data, header, path)

    return FrameInfo(
        path=path,
        telescope_id=tid,
        data=data,
        header=header,
        wcs=wcs,
        noise_adu=noise,
        background=bg,
        snr=snr,
        fwhm_arcsec=float(fwhm),
        pixel_scale_arcsec=float(pixel_scale) if pixel_scale else 0.0,
        exptime=float(exptime),
        weight=0.0,
        saturated_pixels=sat,
        sp_emulator=sp_emulator,
        mask=native_mask,
        mask_sources=mask_sources,
    )
