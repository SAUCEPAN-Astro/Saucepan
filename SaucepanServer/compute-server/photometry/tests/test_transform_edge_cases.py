"""Edge cases for photometry.transform profile loading and apply_transform errors."""

from __future__ import annotations

import pathlib

import pytest
from photometry import transform


def test_profiles_dir_matches_module_constant():
    assert transform.profiles_dir() == transform.PROFILES_DIR


def test_load_profile_not_found_raises(tmp_path):
    with pytest.raises(FileNotFoundError, match="transform profile not found"):
        transform.load_profile("does-not-exist", profiles_root=tmp_path)


def test_load_profile_missing_required_keys_raises(tmp_path):
    path = tmp_path / "bad.yaml"
    path.write_text("profile_id: p1\ntelescope_id: t1\n", encoding="utf-8")
    with pytest.raises(ValueError, match="missing keys"):
        transform.load_profile(path)


def test_load_profile_non_mapping_raises(tmp_path):
    path = tmp_path / "bad.yaml"
    path.write_text("- just\n- a\n- list\n", encoding="utf-8")
    with pytest.raises(ValueError, match="must be a mapping"):
        transform.load_profile(path)


def test_load_profile_unsupported_standard_system_raises(tmp_path):
    path = tmp_path / "bad.yaml"
    path.write_text(
        """
profile_id: p1
telescope_id: t1
standard_system: sdss
band: V
color_term: 0.1
color_zero: 0.5
zp: 25.0
""",
        encoding="utf-8",
    )
    with pytest.raises(ValueError, match="unsupported standard_system"):
        transform.load_profile(path)


def test_load_profile_by_absolute_path_with_suffix(tmp_path):
    path = tmp_path / "custom.yaml"
    path.write_text(
        """
profile_id: p1
telescope_id: t1
standard_system: johnson_cousins
band: V
color_term: 0.1
color_zero: 0.5
zp: 25.0
""",
        encoding="utf-8",
    )
    profile = transform.load_profile(path)
    assert profile["profile_id"] == "p1"
    assert profile["color_index"] == "B-V"  # default filled in
    assert profile["k_extinction"] == 0.0
    assert profile["airmass_policy"] == "ignore"


def test_load_profile_hyphenated_standard_system_normalized(tmp_path):
    path = tmp_path / "hyphen.yaml"
    path.write_text(
        """
profile_id: p1
telescope_id: t1
standard_system: Johnson-Cousins
band: V
color_term: 0.1
color_zero: 0.5
zp: 25.0
""",
        encoding="utf-8",
    )
    profile = transform.load_profile(path)
    assert profile["standard_system"] == "johnson_cousins"


def test_list_profiles_includes_known_fixtures():
    names = transform.list_profiles()
    assert "pier_a_v" in names
    assert "pier_b_v" in names


def test_load_profile_yaml_none_raises_importerror(monkeypatch, tmp_path):
    monkeypatch.setattr(transform, "yaml", None)
    with pytest.raises(ImportError, match="PyYAML required"):
        transform.load_profile("pier_a_v")


# ── apply_transform error branches ──────────────────────────────────────


def _base_profile(**overrides):
    profile = {
        "profile_id": "p1",
        "telescope_id": "t1",
        "standard_system": "johnson_cousins",
        "band": "V",
        "color_term": 0.1,
        "color_zero": 0.5,
        "zp": 25.0,
    }
    profile.update(overrides)
    return profile


def test_apply_transform_first_order_requires_airmass():
    profile = _base_profile(airmass_policy="first_order", k_extinction=0.2)
    with pytest.raises(ValueError, match="airmass required"):
        transform.apply_transform(-10.0, color_index=0.5, profile=profile)


def test_apply_transform_unknown_airmass_policy_raises():
    profile = _base_profile(airmass_policy="bogus-policy")
    with pytest.raises(ValueError, match="unknown airmass_policy"):
        transform.apply_transform(-10.0, color_index=0.5, profile=profile)


def test_apply_transform_zp_override_used_instead_of_profile_zp():
    profile = _base_profile(zp=25.0)
    out = transform.apply_transform(-10.0, color_index=0.5, profile=profile, zp_override=30.0)
    assert out["zp"] == 30.0


def test_apply_transform_no_mag_err_omits_std_mag_err_fields():
    profile = _base_profile()
    out = transform.apply_transform(-10.0, color_index=0.5, profile=profile)
    assert "std_mag_err" not in out
    assert "lp.mag_err" not in out


def test_apply_transform_differential_policy_allowed_no_airmass():
    profile = _base_profile(airmass_policy="differential")
    out = transform.apply_transform(-10.0, color_index=0.5, profile=profile)
    assert out["airmass_corr"] == 0.0
    assert out["airmass_policy"] == "differential"


def test_instrumental_from_truth_round_trips_apply_transform():
    profile = _base_profile(color_term=0.2, color_zero=0.6, zp=24.0)
    inst = transform.instrumental_from_truth(12.0, color_index=0.9, profile=profile)
    out = transform.apply_transform(inst, color_index=0.9, profile=profile)
    assert out["std_mag"] == pytest.approx(12.0, abs=1e-9)


def test_instrumental_from_truth_with_first_order_airmass():
    profile = _base_profile(airmass_policy="first_order", k_extinction=0.15, zp=25.0)
    inst = transform.instrumental_from_truth(12.0, color_index=0.5, profile=profile, airmass=1.5)
    out = transform.apply_transform(inst, color_index=0.5, profile=profile, airmass=1.5)
    assert out["std_mag"] == pytest.approx(12.0, abs=1e-9)
