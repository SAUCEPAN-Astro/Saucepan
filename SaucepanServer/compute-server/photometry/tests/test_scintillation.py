"""Young / Osborn 2015 scintillation prior (approximate) (#203)."""

from __future__ import annotations

import math

import pytest
from photometry import scintillation


def test_matches_young_formula():
    D, X, h, t = 0.35, 1.4, 2400.0, 60.0
    want = (
        0.09
        * D ** (-2.0 / 3.0)
        * X**1.75
        * math.exp(-h / 8000.0)
        * (2.0 * t) ** -0.5
    )
    got = scintillation.sigma_scintillation(D, X, h, t)
    assert got == pytest.approx(want, rel=1e-12)


def test_decreases_with_aperture_and_exposure():
    base = scintillation.sigma_scintillation(0.2, 1.5, 1000.0, 30.0)
    bigger_scope = scintillation.sigma_scintillation(0.8, 1.5, 1000.0, 30.0)
    longer_exp = scintillation.sigma_scintillation(0.2, 1.5, 1000.0, 300.0)
    assert bigger_scope < base
    assert longer_exp < base


def test_increases_with_airmass():
    low = scintillation.sigma_scintillation(0.3, 1.05, 1000.0, 30.0)
    high = scintillation.sigma_scintillation(0.3, 2.5, 1000.0, 30.0)
    assert high > low


def test_turbulence_coeff_scales_linearly():
    a = scintillation.sigma_scintillation(0.3, 1.4, 1000.0, 30.0, turbulence_coeff=1.0)
    b = scintillation.sigma_scintillation(0.3, 1.4, 1000.0, 30.0, turbulence_coeff=1.5)
    assert b == pytest.approx(1.5 * a, rel=1e-12)


def test_none_on_bad_input():
    assert scintillation.sigma_scintillation(0.0, 1.4, 1000.0, 30.0) is None
    assert scintillation.sigma_scintillation(0.3, 0.5, 1000.0, 30.0) is None
    assert scintillation.sigma_scintillation(0.3, 1.4, 1000.0, 0.0) is None
    assert scintillation.sigma_scintillation(None, 1.4, 1000.0, 30.0) is None


def test_mag_form_is_pogson_scaled():
    frac = scintillation.sigma_scintillation(0.3, 1.4, 1000.0, 30.0)
    mag = scintillation.sigma_scint_mag(0.3, 1.4, 1000.0, 30.0)
    assert mag == pytest.approx(1.0857362047581294 * frac, rel=1e-12)
