"""
Optional astropy FITS header reads for grading (Lambda + server-side re-grade).
"""

from __future__ import annotations

import typing

try:
    from astropy.io import fits
except ImportError:  # pragma: no cover — optional extra
    fits = None  # type: ignore[assignment]


def _header_float(hdr: typing.Any, *keys: str) -> float | None:
    for key in keys:
        if key in hdr:
            try:
                return float(hdr[key])
            except (TypeError, ValueError):
                continue
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


def read_sp_headers(path: str) -> dict[str, typing.Any]:
    """Read SP_* and related headers without loading full image data."""
    if fits is None:
        raise ImportError("astropy is required for FITS reads; install saucepan-grading[fits]")

    with fits.open(path, memmap=True) as hdul:
        hdr = hdul[0].header
        return {
            "sp_exptime": _header_float(hdr, "SP_EXPTIME", "EXPTIME"),
            "sp_filter": _header_str(hdr, "SP_FILTER", "FILTER"),
            "sp_fwhm": _header_float(hdr, "SP_FWHM", "SEEING"),
            "sp_calstat": _header_str(hdr, "SP_CALSTAT"),
            "sp_snr": _header_float(hdr, "SP_SNR"),
            "sp_qual": _header_float(hdr, "SP_QUAL"),
            "sp_ra": _header_float(hdr, "SP_RA", "RA"),
            "sp_dec": _header_float(hdr, "SP_DEC", "DEC"),
            "sp_dateobs": _header_str(hdr, "SP_DATEOBS", "DATE-OBS"),
            "sp_emulator": _header_bool(hdr, "SP_EMULATOR"),
        }


# Compatibility alias for callers using the pre-Saucepan name.
read_oa_headers = read_sp_headers
