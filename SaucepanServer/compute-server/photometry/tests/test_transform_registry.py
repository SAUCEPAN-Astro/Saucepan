"""Transform coefficient registry: load-by-hash, path lookup, fail-closed (#206)."""

from __future__ import annotations

import pytest
from photometry import transform


def test_registry_loads_with_versioned_entries():
    reg = transform.load_registry()
    assert reg["transforms"], "registry has no entries"
    for e in reg["transforms"]:
        assert e["version"], f"{e['id']} missing version"
        assert e["from_band"] in reg["passbands"] or e["from_band"] == e["to_band"]
        assert len(e["content_hash"]) == 64


def test_content_hash_is_stable_and_pins_the_entry():
    reg = transform.load_registry()
    e = transform.find_transform("CLEAR", "JC_V")
    # recomputing the hash from the canonical body reproduces it
    assert transform.entry_content_hash(e) == e["content_hash"]
    # and load-by-hash round-trips to the same entry
    back = transform.load_transform_by_hash(e["content_hash"])
    assert back["id"] == e["id"]


def test_find_transform_returns_coeffs_for_registered_path():
    e = transform.find_transform("CLEAR", "JC_V", color_index="B-V")
    assert e["coeffs"]["CI0"] == 0.60
    assert e["to_band"] == "JC_V"


def test_missing_path_fails_closed():
    with pytest.raises(transform.TransformPathError):
        transform.find_transform("JC_V", "SLOAN_I")
    with pytest.raises(transform.TransformPathError):
        transform.load_transform_by_hash("0" * 64)


def test_apply_transform_coeffs_math_and_error_propagation():
    e = transform.find_transform("CLEAR", "JC_V")
    got = transform.apply_transform_coeffs(
        12.000, 0.80, e, zp=25.0, mag_err=0.010, color_err=0.02
    )
    # m_std = 12 + 25 + T*(0.80 - 0.60), T = -0.10
    assert got["std_mag"] == pytest.approx(12.0 + 25.0 + (-0.10) * 0.20, abs=1e-9)
    assert got["transform_hash"] == e["content_hash"]
    # sigma_transform^2 = (0.20*0.03)^2 + (-0.10*0.02)^2
    exp_tr = ((0.20 * 0.03) ** 2 + (0.10 * 0.02) ** 2) ** 0.5
    assert got["sigma_transform"] == pytest.approx(exp_tr, rel=1e-9)
    assert got["std_mag_err"] == pytest.approx((0.010**2 + exp_tr**2) ** 0.5, rel=1e-9)


def test_apply_transform_coeffs_round_trips_through_identity():
    e = transform.find_transform("JC_V", "JC_V")
    got = transform.apply_transform_coeffs(15.3, 0.5, e, zp=0.0)
    assert got["std_mag"] == pytest.approx(15.3, abs=1e-12)
