"""Synthetic dual-bandpass transform tests (#419) + feed into #418 harness."""

from __future__ import annotations

import pathlib
import sys

import pytest

ROOT = pathlib.Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT.parent))

from photometry import consistency, transform

TRUTH_STD_MAG = 12.500
COLOR_INDEX = 0.80  # B-V
MAG_ERR = 0.025


@pytest.fixture
def pier_profiles() -> tuple[dict, dict]:
    a = transform.load_profile("pier_a_v")
    b = transform.load_profile("pier_b_v")
    return a, b


def test_standard_system_is_johnson_cousins(pier_profiles):
    a, b = pier_profiles
    assert a["standard_system"] == transform.STANDARD_SYSTEM == "johnson_cousins"
    assert b["standard_system"] == "johnson_cousins"
    assert a["band"] == b["band"] == "V"
    assert a["color_term"] != b["color_term"]


def test_apply_transform_formula(pier_profiles):
    a, _ = pier_profiles
    inst = transform.instrumental_from_truth(TRUTH_STD_MAG, color_index=COLOR_INDEX, profile=a)
    out = transform.apply_transform(inst, color_index=COLOR_INDEX, profile=a)
    assert out["std_mag"] == pytest.approx(TRUTH_STD_MAG, abs=1e-6)
    assert out["lp.transform_applied"] is True
    assert out["lp.color_term"] == a["color_term"]


def test_dual_bandpass_disagree_before_agree_after_transform(pier_profiles):
    """Two 'V' glasses disagree on instrumental+ZP-only; agree after colour term."""
    a, b = pier_profiles
    inst_a = transform.instrumental_from_truth(TRUTH_STD_MAG, color_index=COLOR_INDEX, profile=a)
    inst_b = transform.instrumental_from_truth(TRUTH_STD_MAG, color_index=COLOR_INDEX, profile=b)

    # ZP-only (no colour term) — residual instrument signature remains
    zp_only_a = inst_a + float(a["zp"])
    zp_only_b = inst_b + float(b["zp"])
    assert abs(zp_only_a - zp_only_b) > MAG_ERR

    report = consistency.evaluate_from_instrumental(
        [
            {
                "telescope_id": "pier-a",
                "inst_mag": inst_a,
                "color_index": COLOR_INDEX,
                "profile": a,
                "mag_err": MAG_ERR,
            },
            {
                "telescope_id": "pier-b",
                "inst_mag": inst_b,
                "color_index": COLOR_INDEX,
                "profile": b,
                "mag_err": MAG_ERR,
            },
        ],
        mag_err=MAG_ERR,
    )
    assert report["pass"] is True
    assert report["n_telescopes"] == 2
    assert report["chi2_red"] <= report["chi2_red_max"]
    assert report["observations"][0]["std_mag"] == pytest.approx(TRUTH_STD_MAG, abs=1e-5)
    assert report["observations"][1]["std_mag"] == pytest.approx(TRUTH_STD_MAG, abs=1e-5)


def test_first_order_airmass_policy():
    profile = {
        "profile_id": "air-test",
        "telescope_id": "pier-x",
        "standard_system": "johnson_cousins",
        "band": "V",
        "color_term": 0.0,
        "color_zero": 0.65,
        "zp": 25.0,
        "k_extinction": 0.15,
        "airmass_policy": "first_order",
    }
    out = transform.apply_transform(-10.0, color_index=0.65, profile=profile, airmass=1.2)
    assert out["std_mag"] == pytest.approx(-10.0 + 25.0 + 0.15 * 1.2, abs=1e-9)
    assert out["airmass_corr"] == pytest.approx(0.18, abs=1e-9)


def test_color_term_uncertainty_propagates():
    """σ_std² = σ_mag² + (CI−CI0)² σ_T² + T² σ_CI²."""
    profile = {
        "profile_id": "err-test",
        "telescope_id": "pier-e",
        "standard_system": "johnson_cousins",
        "band": "V",
        "color_term": 0.20,
        "color_zero": 0.50,
        "zp": 25.0,
        "color_term_err": 0.05,
    }
    mag_err = 0.02
    ci = 0.80
    out = transform.apply_transform(
        -10.0,
        color_index=ci,
        profile=profile,
        mag_err=mag_err,
        color_index_err=0.03,
    )
    d_ci = ci - 0.50
    expected = (mag_err**2 + (d_ci * 0.05) ** 2 + (0.20 * 0.03) ** 2) ** 0.5
    assert out["std_mag_err"] == pytest.approx(expected, abs=1e-12)
    assert out["std_mag_err"] > mag_err


def test_color_term_uncertainty_absent_falls_back_to_mag_err():
    profile = {
        "profile_id": "plain",
        "telescope_id": "pier-p",
        "standard_system": "johnson_cousins",
        "band": "V",
        "color_term": 0.1,
        "color_zero": 0.65,
        "zp": 25.0,
    }
    out = transform.apply_transform(-10.0, color_index=0.9, profile=profile, mag_err=0.025)
    assert out["std_mag_err"] == pytest.approx(0.025, abs=1e-12)


def test_transform_coeff_keys_exposed(pier_profiles):
    a, _ = pier_profiles
    out = transform.apply_transform(-8.0, color_index=0.7, profile=a)
    assert set(out["lp.transform_coeff"].keys()) >= {"T", "CI0", "k", "band"}
