"""Cross-language parity: load golden vectors from SaucepanServer/contracts/grading/."""

from __future__ import annotations

import json
import os
from pathlib import Path

import pytest
from grading import constants
from grading.dimensions import (
    headline_score,
    score_image_quality,
    score_task_fidelity,
    score_timeliness,
)
from grading.points import build_reputation_partial, compute_frame_points, ema_update
from grading.stack_filter import is_stack_eligible


def _vectors_dir() -> Path:
    env = os.environ.get("SP_GRADING_VECTORS")
    if env:
        return Path(env)
    # tests/ → grading/ → compute-server/ → SaucepanServer/
    return Path(__file__).resolve().parents[3] / "contracts" / "grading"


def _load(name: str) -> dict:
    path = _vectors_dir() / name
    assert path.is_file(), f"missing shared grading vectors: {path}"
    return json.loads(path.read_text())


def test_vectors_dir_present():
    d = _vectors_dir()
    assert d.is_dir(), d
    for name in (
        "constants.json",
        "points_vectors.json",
        "reputation_vectors.json",
        "stack_vectors.json",
        "headline_vectors.json",
        "dimensions_vectors.json",
        "grade_ingest_min.json",
    ):
        assert (d / name).is_file(), name


def test_constants_match_snapshot():
    snap = _load("constants.json")["constants"]
    assert constants.BASE_POINTS == snap["BASE_POINTS"]
    assert constants.EXPTIME_CAP_SECONDS == snap["EXPTIME_CAP_SECONDS"]
    assert constants.TENURE_LOG_SCALE == snap["TENURE_LOG_SCALE"]
    assert constants.STACK_ELIGIBLE_MIN_QUALITY == snap["STACK_ELIGIBLE_MIN_QUALITY"]
    assert constants.RELIABILITY_EMA_ALPHA == snap["RELIABILITY_EMA_ALPHA"]
    assert constants.HEADLINE_EMA_ALPHA == snap["HEADLINE_EMA_ALPHA"]
    assert constants.SNR_FULL_CREDIT == snap["SNR_FULL_CREDIT"]
    assert constants.SATURATION_PENALTY_FRACTION == snap["SATURATION_PENALTY_FRACTION"]
    assert constants.NEUTRAL_FWHM_SCORE == snap["NEUTRAL_FWHM_SCORE"]
    assert constants.FILTER_ABSENT_SCORE == snap["FILTER_ABSENT_SCORE"]
    assert constants.CALIBRATION_BONUS == snap["CALIBRATION_BONUS"]
    assert constants.CAPTURE_LATENCY_FULL_SEC == snap["CAPTURE_LATENCY_FULL_SEC"]
    assert constants.CAPTURE_LATENCY_ZERO_SEC == snap["CAPTURE_LATENCY_ZERO_SEC"]
    assert constants.UPLOAD_DURATION_FULL_SEC == snap["UPLOAD_DURATION_FULL_SEC"]
    assert constants.UPLOAD_DURATION_ZERO_SEC == snap["UPLOAD_DURATION_ZERO_SEC"]
    assert constants.TIMELINESS_CAPTURE_WEIGHT == snap["TIMELINESS_CAPTURE_WEIGHT"]
    assert constants.TIMELINESS_UPLOAD_WEIGHT == snap["TIMELINESS_UPLOAD_WEIGHT"]
    assert constants.MISSING_TIMELINESS_SCORE == snap["MISSING_TIMELINESS_SCORE"]
    assert constants.CHEAP_DIMENSION_WEIGHTS == snap["CHEAP_DIMENSION_WEIGHTS"]
    assert constants.IMAGE_QUALITY_WEIGHTS == snap["IMAGE_QUALITY_WEIGHTS"]


@pytest.mark.parametrize(
    "case",
    _load("points_vectors.json")["cases"],
    ids=lambda c: c["name"],
)
def test_points_vectors(case: dict):
    got = compute_frame_points(
        case["grade"],
        case.get("telescope_stats") or {},
        campaign_multiplier=float(case.get("campaign_multiplier", 1.0)),
    )
    expected = case["expected"]
    for key, want in expected.items():
        assert got[key] == want, f"{case['name']}.{key}: got {got[key]!r} want {want!r}"


@pytest.mark.parametrize(
    "case",
    _load("stack_vectors.json")["cases"],
    ids=lambda c: c["name"],
)
def test_stack_vectors(case: dict):
    assert is_stack_eligible(case["dimensions"]) is case["expected"]


@pytest.mark.parametrize(
    "case",
    _load("headline_vectors.json")["cases"],
    ids=lambda c: c["name"],
)
def test_headline_vectors(case: dict):
    assert headline_score(case["dimensions"]) == case["expected"]


@pytest.mark.parametrize(
    "case",
    _load("reputation_vectors.json")["ema_cases"],
    ids=lambda c: c["name"],
)
def test_ema_vectors(case: dict):
    prev = case["previous"]
    got = ema_update(prev, case["sample"], case["alpha"])
    assert got == case["expected"]


@pytest.mark.parametrize(
    "case",
    _load("reputation_vectors.json")["reputation_cases"],
    ids=lambda c: c["name"],
)
def test_reputation_vectors(case: dict):
    got = build_reputation_partial(
        case["existing"],
        headline=case["headline"],
        dimensions_map=case["dimensions"],
        points_earned=case["points_earned"],
        sp_exptime=case["sp_exptime"],
    )
    expected = case["expected"]
    for key, want in expected.items():
        assert got[key] == want, f"{case['name']}.{key}: got {got[key]!r} want {want!r}"


def test_grade_ingest_min_shape():
    # No grade-ingest validator exists in the grading package; this is
    # deliberately fixture-shape coverage, not validator coverage.
    doc = _load("grade_ingest_min.json")
    assert doc["required"], "grade-ingest required collection is empty"
    assert doc["dimension_keys"], "grade-ingest dimension-key collection is empty"
    assert doc["example"], "grade-ingest example collection is empty"
    example = doc["example"]
    for key in doc["required"]:
        assert key in example
    dims = example["dimensions"]
    for key in doc["dimension_keys"]:
        assert "score" in dims[key]


@pytest.mark.parametrize(
    "case",
    _load("dimensions_vectors.json")["image_quality_cases"],
    ids=lambda c: c["name"],
)
def test_image_quality_vectors(case: dict):
    got = score_image_quality(
        case["metrics"], case["headers"], case.get("predicted_psf_arcsec")
    )
    assert got == case["expected"]


@pytest.mark.parametrize(
    "case",
    _load("dimensions_vectors.json")["task_fidelity_cases"],
    ids=lambda c: c["name"],
)
def test_task_fidelity_vectors(case: dict):
    assert score_task_fidelity(case["headers"], case["task_context"]) == case["expected"]


@pytest.mark.parametrize(
    "case",
    _load("dimensions_vectors.json")["timeliness_cases"],
    ids=lambda c: c["name"],
)
def test_timeliness_vectors(case: dict):
    assert score_timeliness(case["task_context"]) == case["expected"]


def test_parity_vector_collections_are_non_empty():
    collections = {
        "points": _load("points_vectors.json")["cases"],
        "stack": _load("stack_vectors.json")["cases"],
        "headline": _load("headline_vectors.json")["cases"],
        "EMA": _load("reputation_vectors.json")["ema_cases"],
        "reputation": _load("reputation_vectors.json")["reputation_cases"],
        "image quality": _load("dimensions_vectors.json")["image_quality_cases"],
        "task fidelity": _load("dimensions_vectors.json")["task_fidelity_cases"],
        "timeliness": _load("dimensions_vectors.json")["timeliness_cases"],
    }
    for name, cases in collections.items():
        assert cases, f"{name} parity-vector collection is empty"
    assert _load("constants.json")["constants"], "constants collection is empty"
