"""
Header reader module for Saucepan pipeline.

Extracts SP_* canonical headers from normalized FITS files.
Each function does one thing - Unix philosophy.
"""

import logging
from dataclasses import dataclass

from astropy.io import fits

logger = logging.getLogger(__name__)


@dataclass
class SPHeaders:
    """Container for SP_* header values extracted from FITS."""

    science_type: str = "imaging"
    filter_name: str = "L"
    calstat: str = "NONE"
    tier: int = 3
    ra: float | None = None
    dec: float | None = None
    exposure_time: float | None = None
    pixscale: float | None = None
    fwhm: float | None = None


def read_sp_headers(fits_path: str) -> SPHeaders:
    """
    Extract SP_* canonical headers from a normalized FITS file.

    Args:
        fits_path: Path to FITS file with SP_* headers

    Returns:
        SPHeaders dataclass with extracted values
    """
    science_type = "imaging"
    filter_name = "L"
    calstat = "NONE"
    tier = 3
    ra = None
    dec = None
    exposure_time = None
    pixscale = None
    fwhm = None

    try:
        with fits.open(fits_path) as hdul:
            header = hdul[0].header

            if "SP_SCTYPE" in header:
                science_type = _clean_str(header["SP_SCTYPE"])
            if "SP_FILTER" in header:
                filter_name = _clean_str(header["SP_FILTER"])
            if "SP_CALSTAT" in header:
                calstat = _clean_str(header["SP_CALSTAT"])
            if "SP_TIER" in header:
                tier = int(header["SP_TIER"])
            if "SP_RA" in header:
                ra = float(header["SP_RA"])
            if "SP_DEC" in header:
                dec = float(header["SP_DEC"])
            if "SP_EXPTIME" in header:
                exposure_time = float(header["SP_EXPTIME"])
            if "SP_PIXSCALE" in header:
                pixscale = float(header["SP_PIXSCALE"])
            if "SP_FWHM" in header:
                fwhm = float(header["SP_FWHM"])

    except Exception as e:
        logger.error(f"Failed to extract SP_ headers from {fits_path}: {e}")

    return SPHeaders(
        science_type=science_type,
        filter_name=filter_name,
        calstat=calstat,
        tier=tier,
        ra=ra,
        dec=dec,
        exposure_time=exposure_time,
        pixscale=pixscale,
        fwhm=fwhm,
    )


def _clean_str(value) -> str:
    """Clean string header value."""
    return value.strip() if isinstance(value, str) else str(value)


def validate_headers(headers: SPHeaders) -> tuple[bool, list[str]]:
    """
    Validate that required headers are present and valid.

    Args:
        headers: SPHeaders to validate

    Returns:
        Tuple of (is_valid, list of error messages)
    """
    errors = []

    if headers.tier == 3:
        errors.append("Input is tier-3 (flagged) - insufficient headers")
    if headers.ra is None or headers.dec is None:
        errors.append("Missing coordinates (SP_RA, SP_DEC)")
    if headers.filter_name is None:
        errors.append("Missing filter name (SP_FILTER)")

    return len(errors) == 0, errors


# Compatibility aliases for callers using the pre-Saucepan names.
OAHeaders = SPHeaders
read_oa_headers = read_sp_headers
