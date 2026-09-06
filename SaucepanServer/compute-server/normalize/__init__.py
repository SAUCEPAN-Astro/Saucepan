"""
Saucepan FITS Normalization Library

Converts heterogeneous FITS files to Saucepan canonical schema.
Works with data from any source: live telescopes, archives, contributed datasets.

Example:
    from normalize import normalize_fits

    result = normalize_fits("input.fits", "output.fits", source="archive")
    if result.success:
        print(f"Tier {result.tier}: {len(result.resolved)} headers resolved")
    else:
        print(f"Error: {result.error}")

Main API:
    - normalize_fits() - Normalize a FITS file
    - NormalizationResult - Result dataclass
    - TIERS - Tier definitions
"""

from normalize.normalize import NormalizationResult, normalize_fits
from normalize.schema import MANDATORY_HEADERS, SP_HEADERS, TIERS

__version__ = "0.2.0"
__all__ = [
    "normalize_fits",
    "NormalizationResult",
    "TIERS",
    "SP_HEADERS",
    "MANDATORY_HEADERS",
]
