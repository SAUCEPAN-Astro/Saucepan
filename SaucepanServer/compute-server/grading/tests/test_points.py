"""Behavioral smoke tests for points helpers (parity gate is test_parity_vectors.py)."""

from __future__ import annotations

import json

from grading.points import build_reputation_partial, compute_frame_points


def _grade(image_quality: float, timeliness: float = 0.8, sp_exptime: float = 30.0) -> dict:
    return {
        "dimensions": {
            "image_quality": {"score": image_quality},
            "task_fidelity": {"score": 0.9},
            "timeliness": {"score": timeliness},
        },
        "sp_exptime": sp_exptime,
    }


def test_points_monotonic_in_quality():
    low = compute_frame_points(_grade(0.0), {"total_exposure_seconds": 0.0})
    high = compute_frame_points(_grade(1.0), {"total_exposure_seconds": 0.0})
    assert high["points_earned"] > low["points_earned"]


def test_reputation_partial_has_source():
    partial = build_reputation_partial(
        None,
        headline=80,
        dimensions_map=_grade(0.7)["dimensions"],
        points_earned=5.5,
        sp_exptime=20.0,
    )
    assert partial["source"] == "grade_ingest"
    assert "last_ingested_at" in partial


def test_nonfinite_points_and_reputation_inputs_are_json_safe():
    points = compute_frame_points(
        _grade(0.8, sp_exptime=float("inf")),
        {"total_exposure_seconds": float("nan")},
        campaign_multiplier=float("inf"),
    )
    partial = build_reputation_partial(
        {"total_points": float("nan")},
        headline=80,
        dimensions_map=_grade(0.7)["dimensions"],
        points_earned=float("inf"),
        sp_exptime=float("nan"),
    )
    json.dumps(points, allow_nan=False)
    json.dumps(partial, allow_nan=False)


def test_large_finite_points_do_not_overflow_json():
    points = compute_frame_points(
        _grade(1.0, sp_exptime=1e308),
        {"total_exposure_seconds": 1e308},
        base_points=1e308,
        campaign_multiplier=1e308,
    )
    json.dumps(points, allow_nan=False)
