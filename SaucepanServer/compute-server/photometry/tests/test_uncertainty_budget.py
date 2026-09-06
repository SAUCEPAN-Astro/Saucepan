"""Full per-measurement error budget: term split + sum + round-trip (#206)."""

from __future__ import annotations

import math

import pytest
from photometry import uncertainty


def test_ccd_error_terms_quadrature_sum_matches_closed_form():
    kw = dict(npix=80.0, sky=120.0, gain=1.6, rdnoise=8.0, sky_err=2.0)
    terms = uncertainty.ccd_error_terms(50_000.0, **kw)
    combined = math.sqrt(sum(v * v for v in terms.values()))
    assert combined == pytest.approx(
        uncertainty.mag_err_from_flux(50_000.0, **kw), rel=1e-12
    )


def test_ccd_error_terms_pure_poisson_has_zero_sky_and_read():
    terms = uncertainty.ccd_error_terms(10_000.0, npix=25.0, gain=1.0)
    assert terms["sky"] == 0.0 and terms["read"] == 0.0
    assert terms["photon"] == pytest.approx(
        1.0857362047581294 / math.sqrt(10_000.0), rel=1e-12
    )


def test_ccd_error_terms_fails_closed():
    assert uncertainty.ccd_error_terms(0.0, npix=10.0) is None


def test_budget_terms_sum_in_quadrature():
    b = uncertainty.uncertainty_budget(
        photon=0.010, sky=0.006, read=0.004, scint=0.003,
        transform=0.008, red=0.005, pier=0.007,
    )
    expected = math.sqrt(
        0.010**2 + 0.006**2 + 0.004**2 + 0.003**2 + 0.008**2 + 0.005**2 + 0.007**2
    )
    assert b["sigma_total"] == pytest.approx(expected, rel=1e-12)
    assert b["missing"] == []
    assert set(b["measured"]) == set(uncertainty.BUDGET_TERMS)
    # per-term breakdown present
    assert b["terms"]["transform"]["var"] == pytest.approx(0.008**2, rel=1e-12)


def test_missing_terms_are_reported_not_zeroed():
    b = uncertainty.uncertainty_budget(photon=0.02, sky=0.01)
    assert b["terms"]["scint"] == {"sigma": None, "var": None}
    assert set(b["missing"]) == {"read", "scint", "transform", "red", "pier"}
    assert b["sigma_total"] == pytest.approx(math.sqrt(0.02**2 + 0.01**2), rel=1e-12)


def test_sigma_sys_is_folded_and_passed_through():
    base = uncertainty.uncertainty_budget(photon=0.02)
    withsys = uncertainty.uncertainty_budget(photon=0.02, sigma_sys=0.015, domain_cell="pier-B/V")
    assert withsys["sigma_sys"] == 0.015
    assert withsys["domain_cell"] == "pier-B/V"
    assert withsys["sigma_total"] == pytest.approx(
        math.sqrt(base["sigma_total"] ** 2 + 0.015**2), rel=1e-12
    )


def test_budget_round_trip_from_ccd_terms():
    terms = uncertainty.ccd_error_terms(
        40_000.0, npix=64.0, sky=90.0, gain=1.5, rdnoise=6.0
    )
    b = uncertainty.uncertainty_budget(**terms, transform=0.01)
    # recomputing sigma_total from the reported per-term vars reproduces it
    total = math.sqrt(sum(t["var"] for t in b["terms"].values() if t["var"] is not None))
    assert total == pytest.approx(b["sigma_total"], rel=1e-12)


def test_negative_or_nonfinite_term_is_treated_as_missing():
    b = uncertainty.uncertainty_budget(photon=0.02, sky=-1.0, read=float("nan"))
    assert "sky" in b["missing"] and "read" in b["missing"]
