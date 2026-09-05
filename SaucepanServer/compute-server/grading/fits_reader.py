"""Optional astropy FITS header reads for grading and server-side re-grade."""

from __future__ import annotations

import math
import typing

from grading.fits_limits import ensure_fits_loadable

try:
    from astropy.io import fits
except ImportError:  # pragma: no cover — optional extra
    fits = None  # type: ignore[assignment]


def _header_float(hdr: typing.Any, *keys: str) -> float | None:
    for key in keys:
        if key in hdr:
            try:
                value = float(hdr[key])
                if math.isfinite(value):
                    return value
            except (TypeError, ValueError):
                pass
    return None


def _header_str(hdr: typing.Any, *keys: str) -> str | None:
    for key in keys:
        if key in hdr:
            val = hdr[key]
            if val is not None and str(val).strip():
                return str(val).strip()
    return None


def _header_bool(hdr: typing.Any, *keys: str) -> bool:
    for key in keys:
        if key in hdr:
            try:
                return bool(int(hdr[key]))
            except (TypeError, ValueError):
                return _truthy_raw(hdr[key])
    return False


def _truthy_raw(val: typing.Any) -> bool:
    if val is True:
        return True
    if val is False or val is None:
        return False
    if isinstance(val, (int, float)):
        return val != 0
    return str(val).strip().lower() in {"1", "true", "t", "yes"}


def _pixel_scale_arcsec(hdr: typing.Any) -> float | None:
    """Read normalized or WCS pixel scale in arcseconds per pixel."""
    explicit = _header_float(hdr, "SP_PIXSCALE", "PIXSCALE")
    if explicit is not None:
        return explicit
    cdelt2 = _header_float(hdr, "CDELT2")
    if cdelt2 is not None:
        return abs(cdelt2) * 3600.0
    cd2_1 = _header_float(hdr, "CD2_1")
    cd2_2 = _header_float(hdr, "CD2_2")
    if cd2_1 is not None or cd2_2 is not None:
        return math.hypot(cd2_1 or 0.0, cd2_2 or 0.0) * 3600.0
    return None


def read_sp_headers(path: str) -> dict[str, typing.Any]:
    """Read SP_* and related headers without loading full image data."""
    if fits is None:
        raise ImportError("astropy is required for FITS reads; install saucepan-grading[fits]")

    with fits.open(path, memmap=True) as hdul:
        hdr = hdul[0].header
        ensure_fits_loadable(path, hdr)
        return {
            "sp_exptime": _header_float(hdr, "SP_EXPTIME", "EXPTIME"),
            "sp_filter": _header_str(hdr, "SP_FILTER", "FILTER"),
            "sp_fwhm": _header_float(hdr, "SP_FWHM", "SEEING"),
            "sp_calstat": _header_str(hdr, "SP_CALSTAT"),
            "sp_snr": _header_float(hdr, "SP_SNR"),
            "sp_qual": _header_float(hdr, "SP_QUAL"),
            "sp_pixscale": _pixel_scale_arcsec(hdr),
            "sp_ra": _header_float(hdr, "SP_RA", "RA"),
            "sp_dec": _header_float(hdr, "SP_DEC", "DEC"),
            "sp_dateobs": _header_str(hdr, "SP_DATEOBS", "DATE-OBS"),
            "sp_emulator": _header_bool(hdr, "SP_EMULATOR"),
            "ctype1": _header_str(hdr, "CTYPE1"),
            "ctype2": _header_str(hdr, "CTYPE2"),
            "crval1": _header_float(hdr, "CRVAL1"),
            "crpix1": _header_float(hdr, "CRPIX1"),
            "crval2": _header_float(hdr, "CRVAL2"),
            "crpix2": _header_float(hdr, "CRPIX2"),
        }
