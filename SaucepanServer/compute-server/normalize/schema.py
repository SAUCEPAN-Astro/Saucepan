"""
schema.py — Saucepan canonical FITS header schema and tier definitions.

Original headers preserved in FITS extension HDU[1].
SP_ canonical headers written to primary HDU[0].

Tier definitions:
  Tier 1 (full):    >= 80% of mandatory headers resolved
  Tier 2 (partial): >= 40% of mandatory headers resolved
  Tier 3 (flagged): < 40% of mandatory headers resolved

Configure thresholds via SP_TIER1_THRESHOLD / SP_TIER2_THRESHOLD env vars.
"""

import os
from dataclasses import dataclass

SP_HEADERS: dict[str, tuple[str, str, bool]] = {
    "SP_NORM": ("str", "Normalization engine version", False),
    "SP_SRC": ("str", "Data source: live|archive|contrib|unknown", False),
    "SP_TIER": ("int", "Tier: 1=full 2=partial 3=flagged", False),
    "SP_CHKSUM": ("str", "SHA-256 of original data array", False),
    "SP_RA": ("float", "RA decimal degrees J2000", True),
    "SP_DEC": ("float", "Dec decimal degrees J2000", True),
    "SP_TGTNAM": ("str", "Human-readable target name", False),
    "SP_TELE": ("str", "Telescope registry ID or free-text", True),
    "SP_INSTR": ("str", "Instrument/camera name", False),
    "SP_FILTER": ("str", "Canonical filter name", True),
    "SP_EXPTIME": ("float", "Exposure time seconds", True),
    "SP_DATEOBS": ("str", "Observation start ISO 8601 UTC", True),
    "SP_MJD": ("float", "Modified Julian Date of exposure start", False),
    "SP_HJD": ("float", "Heliocentric Julian Date of exposure start", False),
    "SP_BJD": ("float", "Barycentric Julian Date of exposure start", False),
    "SP_BJD_PROV": ("str", "BJD clock provenance: GPS|ASSUMED_UTC", False),
    "SP_PROV": ("str", "Inline truncated provenance JSON", False),
    "SP_BINX": ("int", "Horizontal binning factor", False),
    "SP_BINY": ("int", "Vertical binning factor", False),
    "SP_TIMESYS": ("str", "Clock provenance: UTC|NTP|GPS|UNKNOWN", False),
    "SP_GAINISO": ("int", "DSLR ISO setting (OSC only)", False),
    "SP_GAIN": ("float", "Detector gain e-/ADU", False),
    "SP_RDNOISE": ("float", "Read noise electrons", False),
    "SP_AIRMASS": ("float", "Airmass at midpoint", False),
    "SP_SITELAT": ("float", "Site latitude decimal degrees", False),
    "SP_SITELON": ("float", "Site longitude decimal degrees", False),
    "SP_SITEELV": ("float", "Site elevation metres", False),
    "SP_CALSTAT": ("str", "Calibrations applied: B=bias D=dark F=flat or NONE", False),
    "SP_BUNIT": ("str", "Physical unit of array values: electron|adu|iso_adu", False),
    "SP_QUAL": ("float", "Quality score 0.0-1.0 (set by assess stage)", False),
    "SP_FWHM": ("float", "Measured PSF FWHM arcsec (from star fitting)", False),
    "SP_SCTYPE": ("str", "Science type: imaging|photometry|astrometry|spectroscopy", False),
    "SP_PIXSCALE": ("float", "Pixel scale in arcsec per pixel", False),
    # Stacked output headers
    "SP_NFRAMES": ("int", "Number of frames combined in stack", False),
    "SP_NREJECT": ("int", "Number of frames rejected before stacking", False),
    "SP_TPSFFW": ("float", "Target PSF FWHM used for PSF matching in arcsec", False),
    "SP_TINTEG": ("float", "Total integration time of stack in seconds", False),
    "SP_PSCALE": ("float", "Output pixel scale of stacked image in arcsec/pixel", False),
    "SP_STACKMTH": ("str", "Stacking method: weighted_mean|median", False),
    "SP_REPROJ": ("str", "Reprojection interpolation method used", False),
    "SP_WGTMTH": ("str", "Weighting method: inverse_variance|uniform", False),
    "SP_SIGCLIP": ("float", "Sigma-clip threshold used during stacking", False),
    # Science validity / reproducibility headers
    "SP_ASTRMSR": ("float", "RMS astrometric residual of this frame in arcsec", False),
    "SP_BKGRMS": ("float", "Background RMS before subtraction in electrons", False),
    "SP_BKGMED": ("float", "Median background level subtracted in electrons", False),
}

MANDATORY_HEADERS: list[str] = [k for k, (_, _, m) in SP_HEADERS.items() if m]

CANONICAL_FILTERS: list[str] = [
    "U",
    "B",
    "V",
    "R",
    "I",
    "u",
    "g",
    "r",
    "i",
    "z",
    "Ha",
    "SII",
    "OIII",
    "Hb",
    "OI",
    "L",
    "RGB",
    "OSC",
    "UNKNOWN",
]
SCIENCE_TYPES: list[str] = ["imaging", "photometry", "astrometry", "spectroscopy"]
DATA_SOURCES: list[str] = ["live", "archive", "contrib", "unknown"]

TIER1_THRESHOLD: float = float(os.getenv("SP_TIER1_THRESHOLD", "0.8"))
TIER2_THRESHOLD: float = float(os.getenv("SP_TIER2_THRESHOLD", "0.4"))


@dataclass
class Tier:
    """Tier classification with description."""

    number: int
    name: str
    description: str
    threshold: float


TIERS = {
    1: Tier(1, "full", "Fully normalized (≥80% mandatory headers)", TIER1_THRESHOLD),
    2: Tier(2, "partial", "Partially normalized (≥40% mandatory headers)", TIER2_THRESHOLD),
    3: Tier(3, "flagged", "Insufficient headers (<40% mandatory headers)", 0.0),
}


def compute_tier(resolved_mandatory: int, total_mandatory: int) -> int:
    """
    Compute normalization tier based on fraction of resolved mandatory headers.

    Args:
        resolved_mandatory: Count of mandatory headers successfully mapped
        total_mandatory: Total count of mandatory headers

    Returns:
        Tier number: 1=full, 2=partial, 3=flagged
    """
    if total_mandatory == 0:
        return 3
    r = resolved_mandatory / total_mandatory
    if r >= TIER1_THRESHOLD:
        return 1
    if r >= TIER2_THRESHOLD:
        return 2
    return 3
