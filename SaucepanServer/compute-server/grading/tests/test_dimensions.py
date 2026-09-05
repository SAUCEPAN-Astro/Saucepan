"""Unit tests for pure dimension scoring."""

from grading.dimensions import headline_score, score_image_quality, score_timeliness


def test_headline_score_weighted():
    dims = {
        "image_quality": {"score": 1.0},
        "task_fidelity": {"score": 1.0},
        "timeliness": {"score": 1.0},
    }
    assert headline_score(dims) == 100


def test_image_quality_snr_dominant():
    result = score_image_quality(
        {"snr": 50.0, "noise_adu": 1.0, "shape": [100, 100], "saturated_pixels": 0},
        {"sp_fwhm": 2.0},
        predicted_psf_arcsec=2.0,
    )
    assert result["score"] >= 0.8


def test_timeliness_missing_timestamps_neutral():
    result = score_timeliness({})
    assert result["score"] == 0.5
    assert result["capture_latency_sec"] is None
