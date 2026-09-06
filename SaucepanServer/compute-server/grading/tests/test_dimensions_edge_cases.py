"""Edge-case coverage for grading.dimensions pure scoring functions.

Exercises existing branches only — never asserts new math, just pins down
behavior already implemented (parity-sensitive: NEVER change dimensions.py
itself to make a test pass).
"""

from __future__ import annotations

from grading.dimensions import (
    clamp,
    dim_score,
    parse_iso8601,
    score_image_quality,
    score_reliability,
    score_stack_compatibility,
    score_task_fidelity,
    score_timeliness,
)

# ── clamp / dim_score / parse_iso8601 ───────────────────────────────────


def test_clamp_bounds():
    assert clamp(-5.0) == 0.0
    assert clamp(5.0) == 1.0
    assert clamp(0.5) == 0.5
    assert clamp(5.0, lo=-10.0, hi=10.0) == 5.0


def test_dim_score_missing_key_defaults_zero():
    assert dim_score({}, "image_quality") == 0.0


def test_dim_score_non_numeric_score_defaults_zero():
    assert dim_score({"image_quality": {"score": "not-a-number"}}, "image_quality") == 0.0


def test_dim_score_none_dimension_value_defaults_zero():
    assert dim_score({"image_quality": None}, "image_quality") == 0.0


def test_parse_iso8601_none_and_empty():
    assert parse_iso8601(None) is None
    assert parse_iso8601("") is None


def test_parse_iso8601_invalid_string_returns_none():
    assert parse_iso8601("not-a-date") is None


def test_parse_iso8601_z_suffix_parses():
    dt = parse_iso8601("2026-01-01T00:00:00Z")
    assert dt is not None
    assert dt.tzinfo is not None


# ── score_image_quality ──────────────────────────────────────────────────


def test_image_quality_default_shape_when_missing():
    result = score_image_quality({}, {})
    assert result["score"] >= 0.0
    assert result["saturation_fraction"] == 0.0


def test_image_quality_missing_fwhm_and_no_sp_qual_is_neutral():
    result = score_image_quality({"snr": 10.0, "shape": [10, 10], "saturated_pixels": 0}, {})
    assert result["fwhm_source"] == "neutral"
    assert result["fwhm_arcsec"] is None


def test_image_quality_sp_qual_proxy_used_when_no_predicted_psf():
    result = score_image_quality(
        {"snr": 10.0, "shape": [10, 10], "saturated_pixels": 0},
        {"sp_fwhm": 3.0, "sp_qual": 0.9},
    )
    assert result["fwhm_source"] == "sp_qual_proxy"


def test_image_quality_header_source_used_with_predicted_psf():
    result = score_image_quality(
        {"snr": 50.0, "shape": [10, 10], "saturated_pixels": 0},
        {"sp_fwhm": 2.0},
        predicted_psf_arcsec=2.0,
    )
    assert result["fwhm_source"] == "header"


def test_image_quality_full_saturation_penalizes_score():
    total_pixels = 100 * 100
    result = score_image_quality(
        {
            "snr": 50.0,
            "shape": [100, 100],
            "saturated_pixels": total_pixels,  # 100% saturated
        },
        {"sp_fwhm": 2.0},
        predicted_psf_arcsec=2.0,
    )
    assert result["saturation_fraction"] == 1.0
    unsaturated = score_image_quality(
        {"snr": 50.0, "shape": [100, 100], "saturated_pixels": 0},
        {"sp_fwhm": 2.0},
        predicted_psf_arcsec=2.0,
    )
    assert result["score"] < unsaturated["score"]


def test_image_quality_zero_snr_and_zero_predicted_psf():
    result = score_image_quality(
        {"snr": 0.0, "shape": [10, 10], "saturated_pixels": 0},
        {"sp_fwhm": 2.0},
        predicted_psf_arcsec=0.0,  # falsy -> not used, falls to sp_qual/neutral branch
    )
    assert result["snr"] == 0.0
    assert result["fwhm_source"] in {"neutral", "sp_qual_proxy"}


# ── score_task_fidelity ───────────────────────────────────────────────────


def test_task_fidelity_no_requested_exptime_uses_fallback_formula():
    result = score_task_fidelity({"sp_exptime": 30.0}, {})
    assert result["exptime_ratio"] is None


def test_task_fidelity_requested_zero_treated_as_missing():
    result = score_task_fidelity({"sp_exptime": 30.0}, {"integration_time_requested": 0})
    assert result["exptime_ratio"] is None


def test_task_fidelity_filter_match_partial_substring():
    result = score_task_fidelity({"sp_filter": "R"}, {"filter_requested": "R,G,B"})
    assert result["filter_match"] is True


def test_task_fidelity_filter_mismatch():
    result = score_task_fidelity({"sp_filter": "R"}, {"filter_requested": "G"})
    assert result["filter_match"] is False
    assert result["score"] < 1.0


def test_task_fidelity_no_filter_info_gets_absent_score():
    result = score_task_fidelity({}, {})
    assert result["filter_match"] is None
    assert result["filter_requested"] is None
    assert result["filter_actual"] is None


def test_task_fidelity_calibrated_status_gets_bonus():
    calibrated = score_task_fidelity({"sp_calstat": "BDF"}, {})
    uncalibrated = score_task_fidelity({"sp_calstat": "NONE"}, {})
    assert calibrated["score"] >= uncalibrated["score"]
    assert calibrated["calstat"] == "BDF"


def test_task_fidelity_exptime_ratio_clamped_above_one():
    result = score_task_fidelity({"sp_exptime": 120.0}, {"integration_time_requested": 30.0})
    assert result["exptime_ratio"] == 1.0


# ── score_timeliness ──────────────────────────────────────────────────────


def test_timeliness_only_upload_duration_present():
    result = score_timeliness(
        {
            "upload_started_at": "2026-01-01T00:00:00Z",
            "upload_completed_at": "2026-01-01T00:01:00Z",
        }
    )
    assert result["upload_duration_sec"] == 60.0
    # capture_score falls back to MISSING_TIMELINESS_SCORE (0.5) since no
    # assignment_at, and upload_score derives from the measured duration.
    assert result["capture_latency_sec"] is None


def test_timeliness_negative_latency_clamped_to_zero():
    result = score_timeliness(
        {
            "assignment_sent_at": "2026-01-01T00:10:00Z",
            "upload_completed_at": "2026-01-01T00:00:00Z",  # before assignment
        }
    )
    assert result["capture_latency_sec"] == 0.0


def test_timeliness_upload_time_alias_used_when_completed_at_missing():
    result = score_timeliness(
        {
            "assignment_sent_at": "2026-01-01T00:00:00Z",
            "upload_time": "2026-01-01T00:05:00Z",
        }
    )
    assert result["capture_latency_sec"] == 300.0


def test_timeliness_invalid_timestamps_treated_as_missing():
    result = score_timeliness(
        {"assignment_sent_at": "garbage", "upload_completed_at": "also-garbage"}
    )
    assert result["score"] == 0.5
    assert result["capture_latency_sec"] is None


# ── score_stack_compatibility: noise_adu branch (line 216-218 gap) ────────


def test_stack_compat_noise_from_adu_when_snr_missing():
    result = score_stack_compatibility({}, {"snr": None, "noise_adu": 10.0}, {})
    assert result["noise_source"] == "noise_adu"


def test_stack_compat_noise_from_adu_high_value_clamped_zero():
    result = score_stack_compatibility({}, {"snr": None, "noise_adu": 10_000.0}, {})
    assert result["noise_score"] == 0.0
    assert result["noise_source"] == "noise_adu"


def test_stack_compat_snr_zero_falls_through_to_noise_adu():
    result = score_stack_compatibility({}, {"snr": 0.0, "noise_adu": 5.0}, {})
    # snr present but not > 0 -> falls to noise_adu branch
    assert result["noise_source"] == "noise_adu"


def test_stack_compat_max_resolution_alias_for_target_pixscale():
    result = score_stack_compatibility({"sp_pixscale": 1.0}, {}, {"max_resolution": 1.0})
    assert result["pixscale_source"] == "computed"


def test_stack_compat_predicted_psf_arcsec_alias_for_max_psf():
    result = score_stack_compatibility({"sp_fwhm": 2.0}, {}, {"predicted_psf_arcsec": 2.0})
    assert result["fwhm_source"] == "computed"


# ── score_reliability ──────────────────────────────────────────────────────


def test_reliability_no_calstat_defaults_none_uncalibrated():
    result = score_reliability({}, {}, {})
    assert result["calstat"] == "NONE"
    assert result["cal_score"] < 1.0


def test_reliability_plate_solve_partial_headers_not_ok():
    result = score_reliability({"ctype1": "RA---TAN", "crval1": 10.0}, {}, {})  # missing crpix1
    assert result["plate_solve_ok"] == 0


def test_reliability_timeliness_missing_uses_neutral_score():
    result = score_reliability({}, {}, {})
    assert result["timeliness_score"] == 0.5
