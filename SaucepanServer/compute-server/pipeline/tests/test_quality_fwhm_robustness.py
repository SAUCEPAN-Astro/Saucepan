"""Regression for #474's real root cause: quality.py::estimate_fwhm() could
report a wildly wrong per-frame FWHM (24.6"/30.9" seen directly, against a
true ~2.75" seeing) when DAOStarFinder's real-star detections mostly produce
catastrophically bad individual Gaussian2D fits and by chance exactly one
fit survives the (1, 30)px sanity window - a median of one sample has no
robustness, so that lone (often spurious) value became the frame's entire
reported FWHM. That contaminated value then became an epoch's shared
PSF-match target (psf_match.py::select_target_psf() takes the max across
frames), forcing every other, correctly-measured frame to be convolved to
match a fictitious oversized PSF and lose real flux beyond
target_photometry.py's fixed-radius aperture - see the #474 issue thread for
the full instrumented trace against real validation/injection_recovery/
truth.py frames (seed=6, N=4, epochs 2 and 9), which this test's synthetic
scenario is a compact, deterministic proxy for, not a bit-for-bit replay.

Two things were fixed, both exercised here:
  1. DAOStarFinder's own matched-filter kernel now scales with
     pixel_scale_arcsec instead of a hardcoded fwhm=5.0, so it doesn't
     systematically mismatch (and miss) genuinely sharp point sources on
     telescopes with sub-5px native seeing.
  2. estimate_fwhm() now requires >= 3 successful per-star fits before
     trusting their median, falling back to the existing gradient proxy
     otherwise - the same robustness bar the function already applies to
     the raw DAOStarFinder detection count, just extended to fit success.

This module does not import validation/injection_recovery/truth.py (one-way
validation-to-production isolation, see
validation/injection_recovery/tests/test_import_boundary.py); the synthetic
frame builder below is a from-scratch reimplementation, following the same
pattern already used by test_target_anchored_snr.py.
"""

from __future__ import annotations

import numpy as np
from saucepan_pipeline.quality import _estimate_fwhm_gradient, estimate_fwhm

H = W = 96


def _gaussian_star(h: int, w: int, fwhm_px: float, flux: float, x0: float, y0: float) -> np.ndarray:
    sigma = fwhm_px / 2.3548
    yy, xx = np.mgrid[0:h, 0:w]
    g = np.exp(-(((xx - x0) ** 2 + (yy - y0) ** 2) / (2 * sigma**2)))
    return g / g.sum() * flux


def _make_star_field(
    seed: int,
    *,
    n_field_stars: int,
    field_flux_lo: float,
    field_flux_hi: float,
    pixel_scale_arcsec: float,
    true_fwhm_arcsec: float = 2.75,
    target_flux: float = 40000.0,
) -> np.ndarray:
    """One bright, sharp target plus n_field_stars fainter companions -
    same shape as truth.py's _add_field_stars, reimplemented standalone."""
    rng = np.random.default_rng(seed)
    true_fwhm_px = true_fwhm_arcsec / pixel_scale_arcsec
    data = rng.normal(100.0, 2.0, size=(H, W)).astype(np.float64)
    data += _gaussian_star(H, W, true_fwhm_px, target_flux, W / 2.0, H / 2.0)
    for _ in range(n_field_stars):
        fx = rng.uniform(8, W - 8)
        fy = rng.uniform(8, H - 8)
        if np.hypot(fx - W / 2.0, fy - H / 2.0) < 20:
            continue  # keep field stars clear of the target's own core
        flux = rng.uniform(field_flux_lo, field_flux_hi)
        data += _gaussian_star(H, W, true_fwhm_px, flux, fx, fy)
    return data.astype(np.float32)


def test_estimate_fwhm_falls_back_when_too_few_fits_succeed() -> None:
    """Sparse, faint field stars (seed=0) reproduce the shape of the real
    incident: DAOStarFinder detects several candidates, but only the target
    itself yields a valid per-star fit (empirically confirmed via direct
    instrumentation - fwhm_values=[2.36] before this guard existed). A
    single surviving value must not be trusted on its own; the function
    must fall back to the same gradient proxy it already uses when
    DAOStarFinder finds too few candidates in the first place."""
    data = _make_star_field(
        seed=0,
        n_field_stars=15,
        field_flux_lo=300.0,
        field_flux_hi=900.0,
        pixel_scale_arcsec=1.2,
    )

    result = estimate_fwhm(data, pixel_scale_arcsec=1.2)
    expected_fallback = _estimate_fwhm_gradient(data, pixel_scale_arcsec=1.2)

    assert result == expected_fallback
    assert 0.0 <= result <= 36.0  # gradient proxy's own (1, 30)px clamp * scale


def test_estimate_fwhm_stays_sane_with_a_typical_star_field() -> None:
    """A realistic field-star population (matching truth.py's own N=20,
    flux 500-8000 e- density) gives DAOStarFinder enough well-fit companions
    that the per-star median is trustworthy, and it stays within a
    physically plausible range - never the 20-30+ arcsec outliers the
    pre-fix code could return."""
    data = _make_star_field(
        seed=1,
        n_field_stars=20,
        field_flux_lo=500.0,
        field_flux_hi=8000.0,
        pixel_scale_arcsec=1.2,
    )

    result = estimate_fwhm(data, pixel_scale_arcsec=1.2)

    assert 0.5 <= result <= 8.0
