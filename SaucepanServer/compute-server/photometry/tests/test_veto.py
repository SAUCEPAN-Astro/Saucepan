"""Veto ledger + cross-path coadd guard (#206)."""

from __future__ import annotations

import pytest
from photometry import veto


def test_filter_mismatch_is_a_hard_veto():
    policy = {"hard": ["filter_mismatch"], "soft": []}
    m = {"requested_filter": "JC_V", "filter_family": "CLEAR", "transform_path": None}
    out = veto.evaluate_veto(m, policy)
    assert out["allow"] is False
    assert out["channel"] == "dropped"
    assert out["vetoes"][0]["rule"] == "filter_mismatch"
    assert out["vetoes"][0]["level"] == "hard"


def test_registered_transform_path_rescues_filter_mismatch():
    policy = {"hard": ["filter_mismatch"]}
    m = {
        "requested_filter": "JC_V",
        "filter_family": "CLEAR",
        "transform_path": "aavso_generic_clear_to_jc_v_bv",
    }
    assert veto.evaluate_veto(m, policy)["allow"] is True


def test_transform_residual_over_threshold_is_a_soft_flag():
    policy = {"soft": [{"transform_residual_gt": 0.05}]}
    m = {"transform_residual": 0.09}
    out = veto.evaluate_veto(m, policy)
    assert out["allow"] is True  # soft: kept
    assert out["channel"] == "native_only"  # but out of the ensemble
    assert out["vetoes"] == [
        {"rule": "transform_residual_gt", "level": "soft", "value": 0.09, "threshold": 0.05}
    ]


def test_transform_residual_filled_from_transform_stats():
    policy = {"soft": [{"transform_residual_gt": 0.05}]}
    m = {"transform_path": "p1"}
    stats = {"p1": {"residual_rms": 0.2}}
    out = veto.evaluate_veto(m, policy, transform_stats=stats)
    assert out["vetoes"][0]["value"] == pytest.approx(0.2)


def test_zp_rms_and_airmass_and_pier_delta_z_thresholds():
    policy = {
        "hard": [{"zp_rms_gt": 0.15}],
        "soft": [{"airmass_gt": 2.0}, {"pier_delta_z_gt": 0.03}],
    }
    m = {"zp_rms": 0.20, "airmass": 2.5, "pier_delta_z": -0.05}
    out = veto.evaluate_veto(m, policy)
    assert out["allow"] is False
    rules = {v["rule"] for v in out["vetoes"]}
    assert rules == {"zp_rms_gt", "airmass_gt", "pier_delta_z_gt"}


def test_bad_night_flag():
    assert veto.evaluate_veto({"night_flag": "NONPHOT"}, {"hard": ["bad_night"]})["allow"] is False
    assert veto.evaluate_veto({"night_flag": "PHOT"}, {"hard": ["bad_night"]})["allow"] is True


def test_empty_policy_allows_everything():
    out = veto.evaluate_veto({"zp_rms": 9.9}, None)
    assert out == {"allow": True, "vetoes": [], "channel": "ensemble"}


def test_unknown_condition_raises():
    with pytest.raises(ValueError):
        veto.evaluate_veto({}, {"hard": [{"snr_lt": 5}]})


def test_blind_cross_path_coadd_is_refused():
    transformed = {"transform_path": "aavso_generic_clear_to_jc_v_bv"}
    native_v = {"filter_family": "JC_V", "transform_path": None}
    with pytest.raises(veto.CrossPathCoaddError):
        veto.assert_same_transform_path([transformed, native_v])
    # two different transform ids also refused
    with pytest.raises(veto.CrossPathCoaddError):
        veto.assert_same_transform_path(
            [{"transform_path": "a"}, {"transform_path": "b"}]
        )


def test_same_transform_path_coadd_is_allowed():
    a = {"transform_path": "aavso_generic_clear_to_jc_v_bv"}
    b = {"transform_path": "aavso_generic_clear_to_jc_v_bv"}
    key = veto.assert_same_transform_path([a, b])
    assert key == ("transformed", "aavso_generic_clear_to_jc_v_bv")


def test_partition_channels_splits_native_and_transformed():
    ms = [
        {"filter_family": "JC_V", "transform_path": None},
        {"filter_family": "JC_V", "transform_path": None},
        {"transform_path": "aavso_generic_clear_to_jc_v_bv"},
    ]
    parts = veto.partition_channels(ms)
    assert len(parts) == 2
    assert len(parts[("native", "JC_V")]) == 2
