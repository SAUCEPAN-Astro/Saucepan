"""Direct tests for grading.orchestrate.build_grade_payload (no FITS I/O).

build_grade_payload is exercised indirectly by worker/tests/test_grading.py
and by the datalake sync path; this file pins its own field-assembly
contract (idempotency_key derivation, provenance passthrough, catalog
mirror fields) independent of those callers.
"""

from __future__ import annotations

from grading.emulator_policy import classify_frame
from grading.orchestrate import build_grade_payload


def _metrics(**overrides):
    base = {"snr": 30.0, "noise_adu": 4.0, "star_pixels": 100, "saturated_pixels": 0}
    base.update(overrides)
    return base


def _headers(**overrides):
    base = {
        "sp_exptime": 30.0,
        "sp_filter": "V",
        "sp_fwhm": 2.2,
        "sp_calstat": "BDF",
        "sp_snr": 30.0,
        "sp_ra": 10.0,
        "sp_dec": 20.0,
        "sp_dateobs": "2026-01-01T00:00:00",
    }
    base.update(overrides)
    return base


def test_idempotency_key_derived_from_upload_id_when_absent():
    payload = build_grade_payload(
        {"upload_id": "u42"}, quality_metrics=_metrics(), headers=_headers()
    )
    assert payload["idempotency_key"] == "u42:grade"


def test_idempotency_key_explicit_takes_precedence():
    payload = build_grade_payload(
        {"upload_id": "u42", "idempotency_key": "explicit-key"},
        quality_metrics=_metrics(),
        headers=_headers(),
    )
    assert payload["idempotency_key"] == "explicit-key"


def test_idempotency_key_none_when_no_upload_id():
    payload = build_grade_payload({}, quality_metrics=_metrics(), headers=_headers())
    assert payload["idempotency_key"] is None


def test_grader_version_defaults_to_constants_version():
    from grading import constants

    payload = build_grade_payload({}, quality_metrics=_metrics(), headers=_headers())
    assert payload["grader_version"] == constants.GRADER_VERSION


def test_grader_version_override_used():
    payload = build_grade_payload(
        {}, quality_metrics=_metrics(), headers=_headers(), grader_version="9.9.9-test"
    )
    assert payload["grader_version"] == "9.9.9-test"


def test_classification_precomputed_reused_not_recomputed():
    precomputed = classify_frame({"sp_emulator": True}, {})
    payload = build_grade_payload(
        {}, quality_metrics=_metrics(), headers=_headers(), classification=precomputed
    )
    assert payload["sp_emulator"] is True
    assert payload["data_tier"] == "emulator"
    assert payload["science_eligible"] is False


def test_classification_computed_when_absent():
    payload = build_grade_payload(
        {}, quality_metrics=_metrics(), headers=_headers(sp_emulator=None)
    )
    # headers has no sp_emulator key at all in this branch's default -> science
    assert payload["data_tier"] == "science"


def test_dimensions_and_headline_present_with_scientific_value_unscored():
    payload = build_grade_payload({}, quality_metrics=_metrics(), headers=_headers())
    dims = payload["dimensions"]
    assert set(dims) == {
        "image_quality",
        "task_fidelity",
        "timeliness",
        "reliability",
        "scientific_value",
        "stack_compatibility",
    }
    assert dims["scientific_value"] is None
    assert isinstance(payload["headline"], int)


def test_l1_catalog_fields_mirrored_from_headers():
    payload = build_grade_payload({}, quality_metrics=_metrics(), headers=_headers())
    assert payload["sp_ra"] == 10.0
    assert payload["sp_dec"] == 20.0
    assert payload["sp_dateobs"] == "2026-01-01T00:00:00"
    assert payload["sp_filter"] == "V"
    assert payload["sp_fwhm"] == 2.2
    assert payload["sp_calstat"] == "BDF"


def test_object_key_prefers_s3_key_over_object_key():
    payload = build_grade_payload(
        {"s3_key": "s3/path.fits", "object_key": "other/path.fits"},
        quality_metrics=_metrics(),
        headers=_headers(),
    )
    assert payload["object_key"] == "s3/path.fits"


def test_object_key_falls_back_to_object_key_when_no_s3_key():
    payload = build_grade_payload(
        {"object_key": "other/path.fits"},
        quality_metrics=_metrics(),
        headers=_headers(),
    )
    assert payload["object_key"] == "other/path.fits"


def test_quality_metrics_subset_carried_through():
    payload = build_grade_payload(
        {},
        quality_metrics=_metrics(snr=42.0, saturated_pixels=3),
        headers=_headers(),
    )
    assert payload["quality_metrics"]["snr"] == 42.0
    assert payload["quality_metrics"]["saturated_pixels"] == 3


def test_graded_at_is_iso8601_utc_timestamp():
    payload = build_grade_payload({}, quality_metrics=_metrics(), headers=_headers())
    assert "T" in payload["graded_at"]
    assert payload["graded_at"].endswith("+00:00") or payload["graded_at"].endswith("Z")
