"""Atmospheric scintillation noise prior (approximate).

Young (1967) / Osborn et al. (2015, MNRAS 452, 1707) empirical approximation
for the fractional intensity variance from scintillation in a short exposure:

    sigma_scint ~= C_Y * 0.09 * D^(-2/3) * X^1.75 * exp(-h / h0) * (2 * t)^(-1/2)

with D the telescope aperture (m), X the airmass, h the observatory altitude
(m), h0 = 8000 m the atmospheric scale height, and t the exposure time (s).
C_Y is an empirical site/turbulence scaling (Osborn 2015 median ~1.5); default
1.0 keeps it the bare Young form.

This is a *prior* — a floor to fold into the error budget when a frame has no
better per-star estimate. It is not a measurement. Every consumer must label
it approximate (FITS ``SP_SCINT`` carries the ``approx`` comment).
"""

from __future__ import annotations

import math

SCALE_HEIGHT_M = 8000.0
_YOUNG_COEFF = 0.09


def sigma_scintillation(
    aperture_m: float,
    airmass: float,
    altitude_m: float,
    exptime_s: float,
    *,
    turbulence_coeff: float = 1.0,
) -> float | None:
    """Approximate fractional scintillation noise (dimensionless sigma).

    Returns ``None`` if any input is missing or unphysical — callers should
    then simply omit the prior rather than substitute a guess.
    """
    vals = (aperture_m, airmass, altitude_m, exptime_s, turbulence_coeff)
    if any(v is None for v in vals):
        return None
    try:
        aperture_m = float(aperture_m)
        airmass = float(airmass)
        altitude_m = float(altitude_m)
        exptime_s = float(exptime_s)
        turbulence_coeff = float(turbulence_coeff)
    except (TypeError, ValueError):
        return None
    if not all(math.isfinite(v) for v in (aperture_m, airmass, altitude_m, exptime_s)):
        return None
    if aperture_m <= 0.0 or airmass < 1.0 or exptime_s <= 0.0:
        return None

    sigma = (
        turbulence_coeff
        * _YOUNG_COEFF
        * aperture_m ** (-2.0 / 3.0)
        * airmass**1.75
        * math.exp(-max(altitude_m, 0.0) / SCALE_HEIGHT_M)
        * (2.0 * exptime_s) ** (-0.5)
    )
    return sigma if math.isfinite(sigma) and sigma > 0.0 else None


def sigma_scint_mag(
    aperture_m: float,
    airmass: float,
    altitude_m: float,
    exptime_s: float,
    *,
    turbulence_coeff: float = 1.0,
) -> float | None:
    """Scintillation prior expressed as a magnitude error (small-sigma limit)."""
    frac = sigma_scintillation(
        aperture_m, airmass, altitude_m, exptime_s, turbulence_coeff=turbulence_coeff
    )
    if frac is None:
        return None
    from photometry.uncertainty import POGSON

    return POGSON * frac
