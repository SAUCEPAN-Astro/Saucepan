"""Cross-telescope consistency harness (#418) — synthetic multi-telescope fixture.

Acceptance gate reference for stacking children #408 / #409 / #410 / #415:
stages that claim multi-pier commensurability must keep this harness green.

Pass/fail is reduced χ² against the inverse-variance-weighted mean — not
pairwise 1σ (which false-fails ~98% of the time for 5 consistent telescopes).
"""

from __future__ import annotations

import pathlib
import sys

import pytest

# photometry + pipeline packages on path (compute-server layout)
_COMPUTE = pathlib.Path(__file__).resolve().parents[2]
sys.path.insert(0, str(_COMPUTE))
sys.path.insert(0, str(_COMPUTE / "photometry"))
sys.path.insert(0, str(_COMPUTE / "pipeline"))

from photometry import consistency, transform

TRUTH = 11.250
COLOR = 0.55
ERR = 0.03


def _synthetic_pair():
    a = transform.load_profile("pier_a_v")
    b = transform.load_profile("pier_b_v")
    return [
        {
            "telescope_id": "pier-a",
            "inst_mag": transform.instrumental_from_truth(TRUTH, color_index=COLOR, profile=a),
            "color_index": COLOR,
            "profile": a,
            "mag_err": ERR,
        },
        {
            "telescope_id": "pier-b",
            "inst_mag": transform.instrumental_from_truth(TRUTH, color_index=COLOR, profile=b),
            "color_index": COLOR,
            "profile": b,
            "mag_err": ERR,
        },
    ]


def test_synthetic_multi_telescope_fixture_passes_gate():
    report = consistency.evaluate_from_instrumental(_synthetic_pair(), mag_err=ERR)
    assert report["n_telescopes"] >= 2
    assert report["pass"] is True
    assert report["chi2_red"] <= report["chi2_red_max"]
    assert report["residual_scatter"] < ERR
    assert "failure_mode" not in report
    assert set(report["gates"]) >= {"#408", "#409", "#410", "#415"}
    assert report["metric"] == "cross_telescope_reduced_chi2"


def test_five_telescopes_consistent_data_must_pass():
    """The old pairwise-1σ gate fails ~98% of the time for N=5; χ² must pass."""
    # Same truth, honest equal errors, tiny Gaussian noise well within σ.
    # Offsets are << 1σ so χ²_red stays near 0; pairwise 1σ would still be
    # fragile for random draws, so use deterministic sub-σ offsets.
    truth = 12.0
    err = 0.05
    offsets = [-0.02, -0.01, 0.0, 0.01, 0.015]  # all |δ| < err
    observations = [
        {
            "telescope_id": f"pier-{i}",
            "std_mag": truth + off,
            "std_mag_err": err,
            "transform_applied": True,
        }
        for i, off in enumerate(offsets)
    ]
    report = consistency.evaluate_consistency(observations)
    assert report["n_telescopes"] == 5
    assert report["dof"] == 4
    assert report["pass"] is True
    assert report["chi2_red"] <= report["chi2_red_max"]
    # Pairwise diagnostics still present (10 pairs) but must not drive pass.
    assert len(report["pairs"]) == 10
    assert "within_1sigma" in report["pairs"][0]


def test_weighted_mean_prefers_precise_observations():
    observations = [
        {
            "telescope_id": "precise",
            "std_mag": 10.0,
            "std_mag_err": 0.01,
            "transform_applied": True,
        },
        {
            "telescope_id": "loose",
            "std_mag": 11.0,
            "std_mag_err": 1.0,
            "transform_applied": True,
        },
    ]
    report = consistency.evaluate_consistency(observations)
    # Inverse-variance mean ≈ 10.0001, not the arithmetic mid-point 10.5
    assert report["weighted_mean_std_mag"] == pytest.approx(10.00009999, abs=1e-4)
    assert report["mean_std_mag"] == report["weighted_mean_std_mag"]


def test_chi2_gate_fails_on_systematic_outlier():
    observations = [
        {
            "telescope_id": "a",
            "std_mag": 10.0,
            "std_mag_err": 0.02,
            "transform_applied": True,
        },
        {
            "telescope_id": "b",
            "std_mag": 10.0,
            "std_mag_err": 0.02,
            "transform_applied": True,
        },
        {
            "telescope_id": "outlier",
            "std_mag": 10.5,  # 25σ away
            "std_mag_err": 0.02,
            "transform_applied": True,
        },
    ]
    report = consistency.evaluate_consistency(observations)
    assert report["pass"] is False
    assert report["chi2_red"] > report["chi2_red_max"]
    assert report["failure_mode"].startswith("residual_instrument_signature")


def test_sigma_threshold_alias_tightens_gate():
    observations = [
        {
            "telescope_id": "a",
            "std_mag": 10.00,
            "std_mag_err": 0.05,
            "transform_applied": True,
        },
        {
            "telescope_id": "b",
            "std_mag": 10.06,
            "std_mag_err": 0.05,
            "transform_applied": True,
        },
    ]
    loose = consistency.evaluate_consistency(observations, chi2_red_max=2.0)
    tight = consistency.evaluate_consistency(observations, sigma_threshold=0.1)
    assert loose["pass"] is True
    assert tight["pass"] is False
    assert tight["chi2_red_max"] == 0.1


def test_failure_mode_residual_instrument_signature():
    """Same instrumental path without colour-term correction fails the gate."""
    a = transform.load_profile("pier_a_v")
    b = transform.load_profile("pier_b_v")
    # Deliberately apply wrong colour terms (zeros) → residual signature
    broken_a = dict(a, color_term=0.0)
    broken_b = dict(b, color_term=0.0)
    rows = [
        {
            "telescope_id": "pier-a",
            "inst_mag": transform.instrumental_from_truth(TRUTH, color_index=COLOR, profile=a),
            "color_index": COLOR,
            "profile": broken_a,
            "mag_err": 0.01,
        },
        {
            "telescope_id": "pier-b",
            "inst_mag": transform.instrumental_from_truth(TRUTH, color_index=COLOR, profile=b),
            "color_index": COLOR,
            "profile": broken_b,
            "mag_err": 0.01,
        },
    ]
    report = consistency.evaluate_from_instrumental(rows, mag_err=0.01)
    assert report["pass"] is False
    assert report["failure_mode"].startswith("residual_instrument_signature")


def test_evaluate_consistency_requires_two_telescopes():
    with pytest.raises(ValueError, match="≥2"):
        consistency.evaluate_consistency(
            [
                {
                    "telescope_id": "only",
                    "std_mag": 10.0,
                    "std_mag_err": 0.1,
                    "transform_applied": True,
                }
            ]
        )
