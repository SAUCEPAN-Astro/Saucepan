"""Edge cases for grading.catalog_extract row mapping (#33)."""

from __future__ import annotations

from datetime import datetime, timezone

from grading.catalog_extract import (
    _mjd_from_dateobs,
    _parse_date_obs,
    _tier_from_grade,
    catalog_fields_for_ingest,
    row_from_headers_and_grade,
)

# ── _parse_date_obs ────────────────────────────────────────────────────────


def test_parse_date_obs_none_returns_none():
    assert _parse_date_obs(None) is None


def test_parse_date_obs_empty_string_returns_none():
    assert _parse_date_obs("") is None


def test_parse_date_obs_invalid_returns_none():
    assert _parse_date_obs("not-a-date") is None


def test_parse_date_obs_z_suffix():
    dt = _parse_date_obs("2026-01-01T00:00:00Z")
    assert dt is not None
    assert dt.tzinfo is not None


def test_parse_date_obs_naive_gets_utc():
    dt = _parse_date_obs("2026-01-01T00:00:00")
    assert dt.tzinfo == timezone.utc


# ── _mjd_from_dateobs ────────────────────────────────────────────────────


def test_mjd_from_dateobs_none_returns_none():
    assert _mjd_from_dateobs(None) is None


def test_mjd_from_dateobs_datetime_input():
    dt = datetime(2026, 1, 1, tzinfo=timezone.utc)
    mjd = _mjd_from_dateobs(dt)
    assert mjd is not None
    assert mjd > 60000


def test_mjd_from_dateobs_garbage_string_returns_none():
    assert _mjd_from_dateobs("definitely not a date") is None


def test_mjd_from_dateobs_offset_suffix_stripped():
    mjd = _mjd_from_dateobs("2026-01-01T00:00:00-05:00")
    assert mjd is not None


# ── _tier_from_grade ──────────────────────────────────────────────────────


def test_tier_from_grade_missing_data_tier():
    assert _tier_from_grade({}) is None


def test_tier_from_grade_int_passthrough():
    assert _tier_from_grade({"data_tier": 2}) == 2


def test_tier_from_grade_unknown_string_returns_none():
    assert _tier_from_grade({"data_tier": "unknown-tier"}) is None


def test_tier_from_grade_known_strings():
    assert _tier_from_grade({"data_tier": "science"}) == 1
    assert _tier_from_grade({"data_tier": "emulator"}) == 4


# ── row_from_headers_and_grade: fallback branches ──────────────────────────


def test_row_falls_back_to_dimensions_when_headers_missing_fwhm_snr_filter():
    headers = {"sp_ra": 1.0, "sp_dec": 2.0}
    grade = {
        "telescope_id": "tele-1",
        "dimensions": {
            "image_quality": {"fwhm_arcsec": 3.3, "calstat": "BF"},
            "task_fidelity": {"filter_actual": "g"},
        },
        "quality_metrics": {"snr": 12.0},
    }
    row = row_from_headers_and_grade(headers, grade=grade, object_key="x")
    assert row["fwhm_arcsec"] == 3.3
    assert row["snr"] == 12.0
    assert row["filter"] == "g"
    assert row["calstat"] == "BF"


def test_row_filter_requested_fallback_when_actual_missing():
    grade = {
        "telescope_id": "tele-1",
        "dimensions": {"task_fidelity": {"filter_requested": "R"}},
    }
    row = row_from_headers_and_grade({}, grade=grade, object_key="x")
    assert row["filter"] == "R"


def test_row_object_key_defaults_to_upload_path_when_absent():
    row = row_from_headers_and_grade({}, grade={"telescope_id": "t1"}, upload_id="u99")
    assert row["object_key"] == "upload/u99"


def test_row_object_key_unknown_when_no_upload_id_either():
    row = row_from_headers_and_grade({}, grade={"telescope_id": "t1"})
    assert row["object_key"] == "upload/unknown"


def test_row_task_id_prefers_explicit_over_grade():
    row = row_from_headers_and_grade(
        {}, grade={"telescope_id": "t1", "task_id": 5}, task_id=99, object_key="x"
    )
    assert row["task_id"] == "99"


def test_row_task_id_none_when_absent_everywhere():
    row = row_from_headers_and_grade({}, grade={"telescope_id": "t1"}, object_key="x")
    assert row["task_id"] is None


def test_row_grade_none_defaults_to_empty_dict_but_still_requires_telescope_id():
    try:
        row_from_headers_and_grade({}, grade=None, telescope_id=None, object_key="x")
        assert False, "expected ValueError"
    except ValueError:
        pass


def test_row_telescope_id_from_kwarg_overrides_grade():
    row = row_from_headers_and_grade(
        {}, grade={"telescope_id": "from-grade"}, telescope_id="from-kwarg", object_key="x"
    )
    assert row["telescope_id"] == "from-kwarg"


def test_row_dimensions_not_a_dict_is_ignored_safely():
    grade = {"telescope_id": "t1", "dimensions": "not-a-dict"}
    row = row_from_headers_and_grade({}, grade=grade, object_key="x")
    assert row["fwhm_arcsec"] is None
    assert row["filter"] is None


def test_row_quality_metrics_not_a_dict_is_ignored_safely():
    grade = {"telescope_id": "t1", "quality_metrics": "not-a-dict"}
    row = row_from_headers_and_grade({}, grade=grade, object_key="x")
    assert row["snr"] is None


# ── catalog_fields_for_ingest ────────────────────────────────────────────


def test_catalog_fields_for_ingest_serializes_datetime():
    row = row_from_headers_and_grade(
        {"sp_dateobs": "2026-01-01T00:00:00"},
        grade={"telescope_id": "t1"},
        object_key="x",
    )
    ingest = catalog_fields_for_ingest(row)
    assert isinstance(ingest["date_obs"], str)
    assert ingest["date_obs"].startswith("2026-01-01")


def test_catalog_fields_for_ingest_subset_of_keys():
    row = row_from_headers_and_grade({}, grade={"telescope_id": "t1"}, object_key="x")
    ingest = catalog_fields_for_ingest(row)
    assert "grade_json" not in ingest
    assert "id" in ingest
