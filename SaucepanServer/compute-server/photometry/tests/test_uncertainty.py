"""Per-measurement magnitude uncertainty (#203)."""

from __future__ import annotations

import math

import pytest
from photometry import uncertainty


def _analytic(flux, npix, sky, gain, rdnoise, sky_err):
    var = flux / gain + npix * (sky / gain + rdnoise**2) + (npix * sky_err) ** 2
    return 1.0857362047581294 * math.sqrt(var) / flux


def test_matches_analytic_ccd_equation():
    got = uncertainty.mag_err_from_flux(
        50_000.0, npix=80.0, sky=120.0, gain=1.6, rdnoise=8.0, sky_err=2.0
    )
    want = _analytic(50_000.0, 80.0, 120.0, 1.6, 8.0, 2.0)
    assert got == pytest.approx(want, rel=1e-12)


def test_pure_poisson_limit():
    # No sky, no read noise, no sky-estimate error -> sigma = 1.0857 / sqrt(N_e).
    got = uncertainty.mag_err_from_flux(10_000.0, npix=25.0, gain=1.0)
    assert got == pytest.approx(1.0857362047581294 / math.sqrt(10_000.0), rel=1e-12)


def test_monotonic_decreasing_in_flux():
    fluxes = [1e2, 1e3, 1e4, 1e5, 1e6]
    errs = [
        uncertainty.mag_err_from_flux(f, npix=60.0, sky=100.0, gain=2.0, rdnoise=5.0)
        for f in fluxes
    ]
    assert all(math.isfinite(e) for e in errs)
    assert errs == sorted(errs, reverse=True)
    assert all(a > b for a, b in zip(errs, errs[1:]))


def test_fails_closed_on_nonpositive_flux():
    assert uncertainty.mag_err_from_flux(0.0, npix=10.0) is None
    assert uncertainty.mag_err_from_flux(-5.0, npix=10.0) is None


def test_fails_closed_on_nonfinite_input():
    assert uncertainty.mag_err_from_flux(float("nan"), npix=10.0) is None
    assert uncertainty.mag_err_from_flux(1000.0, npix=float("inf")) is None


def test_combine_in_quadrature():
    assert uncertainty.combine_in_quadrature(3.0, 4.0) == pytest.approx(5.0)
    assert uncertainty.combine_in_quadrature(None, None) is None
    assert uncertainty.combine_in_quadrature(2.0, None) == pytest.approx(2.0)


def test_differential_mag_err_shrinks_with_more_comps():
    one = uncertainty.differential_mag_err(0.01, [0.02])
    many = uncertainty.differential_mag_err(0.01, [0.02, 0.02, 0.02, 0.02])
    assert many < one
    # Target-only error is the floor.
    assert many > 0.01


def test_differential_mag_err_no_comps_is_target_error():
    assert uncertainty.differential_mag_err(0.017, []) == pytest.approx(0.017)
