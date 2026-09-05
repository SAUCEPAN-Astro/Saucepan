"""Tests for stack compatibility scoring."""

from grading.dimensions import score_reliability, score_stack_compatibility


def test_stack_compat_high_when_within_limits():
    result = score_stack_compatibility(
        {"sp_fwhm": 1.5, "sp_pixscale": 1.2},
        {"snr": 60.0, "noise_adu": 12.0},
        {"max_psf_fwhm": 2.0, "contrib_pixscale": 1.2},
    )
    assert result["score"] >= 0.9
    assert result["fwhm_source"] == "computed"
    assert result["pixscale_source"] == "computed"


def test_stack_compat_neutral_when_limits_missing():
    result = score_stack_compatibility({}, {}, {})
    assert result["score"] == 0.5
    assert result["fwhm_source"] == "missing"
    assert result["pixscale_source"] == "missing"
    assert result["noise_source"] == "missing"


def test_stack_compat_penalizes_poor_fwhm():
    good = score_stack_compatibility(
        {"sp_fwhm": 1.0},
        {"snr": 40.0},
        {"max_psf_fwhm": 2.0},
    )
    poor = score_stack_compatibility(
        {"sp_fwhm": 4.0},
        {"snr": 40.0},
        {"max_psf_fwhm": 2.0},
    )
    assert good["score"] > poor["score"]


def test_reliability_prefers_calibrated_plate_solved():
    strong = score_reliability(
        {
            "sp_calstat": "BDF",
            "ctype1": "RA---TAN",
            "ctype2": "DEC--TAN",
            "crval1": 10.0,
            "crpix1": 512.0,
            "crval2": 20.0,
            "crpix2": 512.0,
        },
        {},
        {
            "assignment_sent_at": "2026-01-01T00:00:00Z",
            "upload_completed_at": "2026-01-01T00:05:00Z",
        },
    )
    weak = score_reliability({"sp_calstat": "NONE"}, {}, {})
    assert strong["score"] > weak["score"]
    assert strong["plate_solve_ok"] == 1
    assert weak["plate_solve_ok"] == 0
