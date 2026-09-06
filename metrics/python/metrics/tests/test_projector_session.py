"""Tests for metrics.projectors.session — rollup_night edge cases."""

from __future__ import annotations

from metrics.projectors.session import rollup_night


def test_rollup_night_zero_frames():
    out = rollup_night("T1", "T1_2026-01-01", frames=[])
    assert out["session.frames"] == 0
    assert out["session.exptime_total"] == 0.0
    assert out["session.reject_fraction"] == 0.0
    assert out["session.phot_class"] == "UNKNOWN"
    assert "session.zp_median" not in out
    assert "session.fwhm_rms_pct" not in out


def test_rollup_night_none_frames_defaults_to_empty():
    out = rollup_night("T1", "night", frames=None)
    assert out["session.frames"] == 0


def test_rollup_night_single_frame_airmass_range_zero():
    out = rollup_night("T1", "night", frames=[{"airmass": 1.2, "zp": 22.0}])
    assert out["session.airmass_range"] == 0.0
    assert out["session.zp_drift"] == 0.0
    assert out["session.zp_median"] == 22.0


def test_rollup_night_fwhm_rms_requires_two_points():
    out = rollup_night("T1", "night", frames=[{"fwhm_arcsec": 2.0}])
    assert "session.fwhm_rms_pct" not in out


def test_rollup_night_fwhm_rms_computed_with_two_points():
    out = rollup_night(
        "T1",
        "night",
        frames=[{"fwhm_arcsec": 2.0}, {"fwhm_arcsec": 3.0}],
    )
    assert out["session.fwhm_rms_pct"] > 0


def test_rollup_night_phot_class_boundaries():
    # clear_max_drift_mag default 0.0, thin_cloud_max_drift_mag default 0.3
    phot = rollup_night("T1", "n", frames=[{"zp": 22.0}, {"zp": 22.0}])
    assert phot["session.phot_class"] == "PHOT"

    nonphot = rollup_night("T1", "n", frames=[{"zp": 22.0}, {"zp": 22.2}])
    assert nonphot["session.phot_class"] == "NONPHOT"

    reject = rollup_night("T1", "n", frames=[{"zp": 22.0}, {"zp": 23.0}])
    assert reject["session.phot_class"] == "REJECT"


def test_rollup_night_reject_fraction_computed():
    out = rollup_night(
        "T1",
        "n",
        frames=[{"rejected": True}, {"stack_eligible": False}, {"zp": 22.0}],
    )
    assert out["session.reject_fraction"] == 2 / 3


def test_rollup_night_plate_solve_success_rate_only_when_attempted():
    out = rollup_night("T1", "n", frames=[{"zp": 22.0}])
    assert "session.plate_solve_success_rate" not in out

    out2 = rollup_night(
        "T1",
        "n",
        frames=[{"plate_solve_ok": True}, {"plate_solve_ok": False}],
    )
    assert out2["session.plate_solve_success_rate"] == 0.5


def test_rollup_night_accepts_prefixed_frame_keys():
    out = rollup_night(
        "T1",
        "n",
        frames=[{"frame.zp": 22.0, "frame.airmass": 1.1, "frame.fwhm_arcsec": 2.0}],
    )
    assert out["session.zp_median"] == 22.0
    assert out["session.airmass_range"] == 0.0


def test_rollup_night_extinction_needs_two_points_with_varying_airmass():
    # Single pair -> insufficient.
    out = rollup_night("T1", "n", frames=[{"zp": 22.0, "airmass": 1.2}])
    assert "frame.extinction_coeff" not in out

    # Two points with distinct airmass -> extinction computed.
    out2 = rollup_night(
        "T1",
        "n",
        frames=[
            {"zp": 22.0, "airmass": 1.0},
            {"zp": 21.8, "airmass": 1.5},
        ],
    )
    assert "frame.extinction_coeff" in out2
    assert out2["frame.extinction_coeff"] > 0  # ZP drops as airmass increases


def test_rollup_night_extinction_zero_variance_airmass_returns_none():
    # Both frames share airmass -> var_x == 0 -> extinction None (guarded).
    out = rollup_night(
        "T1",
        "n",
        frames=[
            {"zp": 22.0, "airmass": 1.2},
            {"zp": 21.5, "airmass": 1.2},
        ],
    )
    assert "frame.extinction_coeff" not in out


def test_rollup_night_comp_rms_mag_uses_median():
    out = rollup_night(
        "T1",
        "n",
        frames=[{"comp_rms_mag": 0.01}, {"comp_rms_mag": 0.03}],
    )
    assert out["session.comp_rms_mag"] == 0.02


def test_rollup_night_optional_moon_sep_uses_min_not_median():
    out = rollup_night(
        "T1",
        "n",
        frames=[{"moon_sep_min": 40.0}, {"moon_sep_min": 10.0}],
    )
    assert out["session.moon_sep_min"] == 10.0


def test_rollup_night_malformed_metric_values_are_skipped():
    out = rollup_night(
        "T1",
        "n",
        frames=[{"zp": "not-a-number"}, {"zp": 22.0}],
    )
    assert out["session.zp_median"] == 22.0
