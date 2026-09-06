"""Edge cases for photometry.consistency validation / error branches (#418)."""

from __future__ import annotations

import pytest
from photometry import consistency


def _obs(**overrides):
    base = {
        "telescope_id": "pier-a",
        "std_mag": 12.5,
        "std_mag_err": 0.02,
        "transform_applied": True,
    }
    base.update(overrides)
    return base


def test_requires_at_least_two_observations():
    with pytest.raises(ValueError, match="at least|≥2|requires"):
        consistency.evaluate_consistency([_obs()])


def test_zero_observations_also_rejected():
    with pytest.raises(ValueError):
        consistency.evaluate_consistency([])


def test_cannot_pass_both_chi2_red_max_and_sigma_threshold():
    with pytest.raises(ValueError, match="only one"):
        consistency.evaluate_consistency(
            [_obs(), _obs(telescope_id="pier-b")],
            chi2_red_max=2.0,
            sigma_threshold=3.0,
        )


def test_non_positive_threshold_rejected():
    with pytest.raises(ValueError, match="must be > 0"):
        consistency.evaluate_consistency([_obs(), _obs(telescope_id="pier-b")], chi2_red_max=0.0)


def test_negative_sigma_threshold_rejected():
    with pytest.raises(ValueError, match="must be > 0"):
        consistency.evaluate_consistency(
            [_obs(), _obs(telescope_id="pier-b")], sigma_threshold=-1.0
        )


def test_missing_std_mag_raises():
    bad = {"telescope_id": "pier-a", "std_mag_err": 0.02, "transform_applied": True}
    with pytest.raises(ValueError, match="missing std_mag"):
        consistency.evaluate_consistency([bad, _obs(telescope_id="pier-b")])


def test_missing_std_mag_err_raises():
    bad = {"telescope_id": "pier-a", "std_mag": 12.5, "transform_applied": True}
    with pytest.raises(ValueError, match="missing std_mag"):
        consistency.evaluate_consistency([bad, _obs(telescope_id="pier-b")])


def test_missing_telescope_id_raises():
    bad = {"std_mag": 12.5, "std_mag_err": 0.02, "transform_applied": True}
    with pytest.raises(ValueError, match="telescope_id"):
        consistency.evaluate_consistency([bad, _obs(telescope_id="pier-b")])


def test_require_transform_true_but_missing_flag_raises():
    bad = _obs(transform_applied=False)
    with pytest.raises(ValueError, match="transform_applied"):
        consistency.evaluate_consistency([bad, _obs(telescope_id="pier-b")])


def test_require_transform_false_allows_missing_flag():
    bad = {"telescope_id": "pier-a", "std_mag": 12.5, "std_mag_err": 0.02}
    report = consistency.evaluate_consistency(
        [bad, _obs(telescope_id="pier-b")], require_transform=False
    )
    assert report["n_telescopes"] == 2


def test_zero_std_mag_err_raises():
    bad = _obs(std_mag_err=0.0)
    with pytest.raises(ValueError, match="must be > 0"):
        consistency.evaluate_consistency([bad, _obs(telescope_id="pier-b")])


def test_negative_std_mag_err_raises():
    bad = _obs(std_mag_err=-0.5)
    with pytest.raises(ValueError, match="must be > 0"):
        consistency.evaluate_consistency([bad, _obs(telescope_id="pier-b")])


def test_nan_std_mag_err_raises():
    bad = _obs(std_mag_err=float("nan"))
    with pytest.raises(ValueError, match="must be > 0"):
        consistency.evaluate_consistency([bad, _obs(telescope_id="pier-b")])


def test_three_telescope_consistent_observations_pass():
    obs = [
        _obs(telescope_id="a", std_mag=12.50),
        _obs(telescope_id="b", std_mag=12.51),
        _obs(telescope_id="c", std_mag=12.49),
    ]
    report = consistency.evaluate_consistency(obs)
    assert report["n_telescopes"] == 3
    assert report["dof"] == 2
    assert len(report["pairs"]) == 3


def test_grossly_inconsistent_observations_fail_gate():
    obs = [
        _obs(telescope_id="a", std_mag=10.0),
        _obs(telescope_id="b", std_mag=15.0),
    ]
    report = consistency.evaluate_consistency(obs)
    assert report["pass"] is False
    assert "failure_mode" in report


def test_evaluate_from_instrumental_telescope_id_falls_back_to_profile():
    profile = {
        "profile_id": "p1",
        "telescope_id": "profile-tele",
        "standard_system": "johnson_cousins",
        "band": "V",
        "color_term": 0.1,
        "color_zero": 0.5,
        "zp": 25.0,
    }
    report = consistency.evaluate_from_instrumental(
        [
            {"inst_mag": -10.0, "color_index": 0.8, "profile": profile},
            {
                "telescope_id": "explicit-b",
                "inst_mag": -10.0,
                "color_index": 0.8,
                "profile": profile,
            },
        ]
    )
    ids = [o["telescope_id"] for o in report["observations"]]
    assert "profile-tele" in ids
    assert "explicit-b" in ids
