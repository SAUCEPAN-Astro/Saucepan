"""Tests for frame_catalog row extraction (#33)."""

from __future__ import annotations

from grading.catalog_extract import catalog_fields_for_ingest, row_from_headers_and_grade


def test_row_from_headers_and_grade_maps_sky_time():
    headers = {
        "sp_ra": 83.633,
        "sp_dec": 22.0145,
        "sp_dateobs": "2024-01-15T22:30:00",
        "sp_filter": "r",
        "sp_exptime": 30.0,
        "sp_fwhm": 2.1,
        "sp_snr": 45.0,
        "sp_calstat": "BDF",
    }
    grade = {
        "upload_id": "up-1",
        "telescope_id": "node_a",
        "task_id": 42,
        "headline": 80,
        "stack_eligible": True,
        "sp_exptime": 30.0,
        "data_tier": "science",
        "dimensions": {
            "image_quality": {"fwhm_arcsec": 2.1, "calstat": "BDF"},
            "task_fidelity": {"filter_actual": "r"},
        },
        "quality_metrics": {"snr": 45.0},
    }
    row = row_from_headers_and_grade(
        headers,
        grade=grade,
        campaign_id="camp-1",
        frame_id="frame-1",
        object_key="fits/a.fits",
        checksum_sha256="abc",
        zp=22.5,
    )
    assert row["ra_deg"] == 83.633
    assert row["dec_deg"] == 22.0145
    assert row["filter"] == "r"
    assert row["exptime_sec"] == 30.0
    assert row["fwhm_arcsec"] == 2.1
    assert row["snr"] == 45.0
    assert row["telescope_id"] == "node_a"
    assert row["campaign_id"] == "camp-1"
    assert row["headline_grade"] == 80
    assert row["stack_eligible"] is True
    assert row["zp"] == 22.5
    assert row["date_obs"] is not None
    assert row["mjd_obs"] is not None

    ingest = catalog_fields_for_ingest(row)
    assert ingest["ra_deg"] == 83.633
    assert isinstance(ingest["date_obs"], str)


def test_row_requires_telescope_id():
    try:
        row_from_headers_and_grade({"sp_ra": 1.0}, grade={}, object_key="x")
        assert False, "expected ValueError"
    except ValueError as exc:
        assert "telescope_id" in str(exc)
