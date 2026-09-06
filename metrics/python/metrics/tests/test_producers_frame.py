"""Tests for frame_headers, frame_photometry, and grade producers."""

from __future__ import annotations

import metrics.producers.frame_headers as frame_headers
import metrics.producers.frame_photometry as frame_photometry
import metrics.producers.grade as grade
from metrics.observation import EntityContext

# ---------------------------------------------------------------------------
# frame_headers
# ---------------------------------------------------------------------------


def test_frame_headers_produce_no_staged_path_returns_empty():
    assert frame_headers.produce({}) == {}


def test_frame_headers_produce_maps_known_fields(monkeypatch):
    monkeypatch.setattr(
        frame_headers,
        "_read_headers",
        lambda path: {
            "sp_ra": 83.6,
            "sp_dec": 22.0,
            "sp_tele": "T1",
            "sp_filter": "V",
            "sp_exptime": 60.0,
        },
    )
    ctx: EntityContext = {"staged_path": "/tmp/f.fits"}
    out = frame_headers.produce(ctx)
    assert out["frame.ra_deg"] == 83.6
    assert out["frame.dec_deg"] == 22.0
    assert out["frame.tele_id"] == "T1"
    assert out["frame.filter"] == "V"
    assert out["frame.exptime_sec"] == 60.0
    # No WCS plate-solve keys present -> not solved.
    assert out["frame.plate_solve_ok"] == 0


def test_frame_headers_tele_id_falls_back_to_ctx_telescope_id(monkeypatch):
    monkeypatch.setattr(frame_headers, "_read_headers", lambda path: {})
    ctx: EntityContext = {"staged_path": "/tmp/f.fits", "telescope_id": "T-CTX"}
    out = frame_headers.produce(ctx)
    assert out["frame.tele_id"] == "T-CTX"


def test_frame_headers_plate_solve_ok_when_wcs_present(monkeypatch):
    monkeypatch.setattr(
        frame_headers,
        "_read_headers",
        lambda path: {"ctype1": "RA---TAN", "crval1": 1.0, "crpix1": 512.0},
    )
    ctx: EntityContext = {"staged_path": "/tmp/f.fits"}
    out = frame_headers.produce(ctx)
    assert out["frame.plate_solve_ok"] == 1


def test_frame_headers_wcs_distortion_from_astrmsr(monkeypatch):
    monkeypatch.setattr(frame_headers, "_read_headers", lambda path: {"sp_astrmsr": 0.25})
    ctx: EntityContext = {"staged_path": "/tmp/f.fits"}
    out = frame_headers.produce(ctx)
    assert out["frame.wcs_distortion"] == 0.25
    assert out["frame.astrmsr_arcsec"] == 0.25


def test_frame_headers_derived_background_requires_electron_bunit(monkeypatch):
    monkeypatch.setattr(
        frame_headers,
        "_read_headers",
        lambda path: {"sp_bunit": "adu", "sp_bkgmed": 100.0, "sp_bkgrms": 5.0},
    )
    ctx: EntityContext = {"staged_path": "/tmp/f.fits"}
    out = frame_headers.produce(ctx)
    assert "frame.bkgmed" not in out
    assert "frame.bkgrms" not in out


def test_frame_headers_derived_background_present_when_electron(monkeypatch):
    monkeypatch.setattr(
        frame_headers,
        "_read_headers",
        lambda path: {"sp_bunit": "electron", "sp_bkgmed": 100.0, "sp_bkgrms": 5.0},
    )
    ctx: EntityContext = {"staged_path": "/tmp/f.fits"}
    out = frame_headers.produce(ctx)
    assert out["frame.bkgmed"] == 100.0
    assert out["frame.bkgrms"] == 5.0


def test_frame_headers_background_falls_back_to_bgmd_alias(monkeypatch):
    monkeypatch.setattr(
        frame_headers,
        "_read_headers",
        lambda path: {"sp_bunit": "electron", "sp_bgmd": 42.0},
    )
    ctx: EntityContext = {"staged_path": "/tmp/f.fits"}
    out = frame_headers.produce(ctx)
    assert out["frame.bkgmed"] == 42.0


def test_frame_headers_prov_uri_set_when_sp_prov_present(monkeypatch):
    monkeypatch.setattr(frame_headers, "_read_headers", lambda path: {"sp_prov": '{"src":"live"}'})
    ctx: EntityContext = {"staged_path": "/tmp/f.fits"}
    out = frame_headers.produce(ctx)
    assert out["frame.prov_uri"] == "fits://header#SP_PROV"


def test_frame_headers_prov_uri_absent_without_sp_prov(monkeypatch):
    monkeypatch.setattr(frame_headers, "_read_headers", lambda path: {})
    ctx: EntityContext = {"staged_path": "/tmp/f.fits"}
    out = frame_headers.produce(ctx)
    assert "frame.prov_uri" not in out


def test_frame_headers_none_values_excluded(monkeypatch):
    monkeypatch.setattr(frame_headers, "_read_headers", lambda path: {"sp_ra": None, "sp_dec": 1.0})
    ctx: EntityContext = {"staged_path": "/tmp/f.fits"}
    out = frame_headers.produce(ctx)
    assert "frame.ra_deg" not in out
    assert out["frame.dec_deg"] == 1.0


# ---------------------------------------------------------------------------
# frame_photometry
# ---------------------------------------------------------------------------


def test_frame_photometry_no_result_no_path_returns_empty():
    assert frame_photometry.produce({}) == {}


def test_frame_photometry_prefers_photometry_result():
    ctx: EntityContext = {
        "_photometry_result": {"zp": 22.5, "zp_rms": 0.02, "phot_flag": "ok"},
        "staged_path": "/tmp/should_not_be_read.fits",
    }
    out = frame_photometry.produce(ctx)
    assert out == {"frame.zp": 22.5, "frame.zp_rms": 0.02, "frame.phot_flag": "ok"}


def test_frame_photometry_empty_result_falls_through_to_headers(monkeypatch):
    monkeypatch.setattr(frame_photometry, "_read_headers", lambda path: {"sp_zp": 21.0})
    ctx: EntityContext = {"_photometry_result": {}, "staged_path": "/tmp/f.fits"}
    out = frame_photometry.produce(ctx)
    assert out == {"frame.zp": 21.0}


def test_frame_photometry_non_dict_result_falls_through_to_headers(monkeypatch):
    monkeypatch.setattr(frame_photometry, "_read_headers", lambda path: {"sp_zp": 21.0})
    ctx: EntityContext = {"_photometry_result": "not-a-dict", "staged_path": "/tmp/f.fits"}
    out = frame_photometry.produce(ctx)
    assert out == {"frame.zp": 21.0}


def test_frame_photometry_headers_empty_on_missing_file():
    ctx: EntityContext = {"staged_path": "/nonexistent/path.fits"}
    # astropy.io.fits.open on a missing file raises OSError, caught -> {}.
    out = frame_photometry.produce(ctx)
    assert out == {}


def test_frame_photometry_result_partial_keys_only_mapped_present():
    ctx: EntityContext = {"_photometry_result": {"skymag": 19.2}}
    out = frame_photometry.produce(ctx)
    assert out == {"frame.skymag": 19.2}


# ---------------------------------------------------------------------------
# grade
# ---------------------------------------------------------------------------


def test_grade_produce_no_result_no_path_returns_empty():
    assert grade.produce({}) == {}


def test_grade_produce_uses_grade_result_dict():
    ctx: EntityContext = {
        "_grade_result": {
            "headline": "A",
            "grader_version": "1.0",
            "stack_eligible": True,
            "dimensions": {
                "image_quality": {"score": 0.9, "snr": 40.0, "fwhm_arcsec": 2.1},
                "task_fidelity": {"score": 0.8, "filter_match": True},
            },
            "points_breakdown": {"points_earned": 12.5, "base_points": 10},
        }
    }
    out = grade.produce(ctx)
    assert out["grade.headline"] == "A"
    assert out["grade.version"] == "1.0"
    assert out["grade.stack_eligible"] is True
    assert out["grade.image_quality_score"] == 0.9
    assert out["grade.snr"] == 40.0
    assert out["grade.fwhm_arcsec"] == 2.1
    assert out["grade.task_fidelity_score"] == 0.8
    assert out["grade.filter_match"] is True
    assert out["grade.points_earned"] == 12.5
    assert out["grade.points_base"] == 10
    # None-valued fields are dropped entirely.
    assert "grade.reliability_score" not in out


def test_grade_produce_no_dimensions_still_returns_top_level():
    ctx: EntityContext = {"_grade_result": {"headline": "B"}}
    out = grade.produce(ctx)
    assert out == {"grade.headline": "B"}


def test_grade_reputation_from_grade_payload():
    ctx: EntityContext = {
        "_grade_result": {
            "headline": "A",
            "reputation_partial": {"reliability_score": 0.95, "total_points": 500},
        }
    }
    out = grade.produce(ctx)
    assert out["grade.reliability_ema"] == 0.95
    assert out["grade.reputation_total_points"] == 500


def test_grade_reputation_falls_back_to_ctx_reputation_stats():
    ctx: EntityContext = {
        "_grade_result": {"headline": "A"},
        "reputation_stats": {"task_quality_score": 0.7},
    }
    out = grade.produce(ctx)
    assert out["grade.task_quality_ema"] == 0.7


def test_grade_produce_no_path_and_no_result_returns_empty():
    ctx: EntityContext = {"upload_id": "u1"}
    assert grade.produce(ctx) == {}


def test_grade_produce_grade_fits_returns_none_yields_empty(monkeypatch):
    monkeypatch.setattr(grade, "_grade_fits", lambda path, ctx: None)
    ctx: EntityContext = {"staged_path": "/tmp/f.fits"}
    assert grade.produce(ctx) == {}


def test_grade_produce_grade_fits_dispatches_to_from_grade(monkeypatch):
    monkeypatch.setattr(
        grade, "_grade_fits", lambda path, ctx: {"headline": "C", "grader_version": "2.0"}
    )
    ctx: EntityContext = {"staged_path": "/tmp/f.fits"}
    out = grade.produce(ctx)
    assert out == {"grade.headline": "C", "grade.version": "2.0"}
