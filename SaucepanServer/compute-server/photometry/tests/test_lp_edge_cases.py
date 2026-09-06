"""Edge-case coverage for photometry.lp.run_lp (aperture photometry stub, #422)."""

from __future__ import annotations

import numpy as np
import pytest
from astropy.io import fits
from photometry import lp, transform


def _write_fits(path, data: np.ndarray) -> None:
    hdu = fits.PrimaryHDU(data=data.astype(np.float32))
    hdu.writeto(path, overwrite=True)


def _flat_frame(shape=(50, 50), level=10.0) -> np.ndarray:
    return np.full(shape, level, dtype=np.float32)


# ── skip / error branches ──────────────────────────────────────────────


def test_no_path_and_no_stars_skipped():
    result = lp.run_lp({})
    assert result["status"] == "skipped"
    assert result["reason"] == "no_path_or_comp_stars"


def test_stars_but_no_path_skipped():
    result = lp.run_lp({"campaign_comp_stars": [{"id": "c1", "x": 5, "y": 5}]})
    assert result["status"] == "skipped"


def test_path_but_no_stars_skipped(tmp_path):
    path = tmp_path / "frame.fits"
    _write_fits(path, _flat_frame())
    result = lp.run_lp({"staged_path": str(path)})
    assert result["status"] == "skipped"


def test_load_data_failure_sets_error_status():
    result = lp.run_lp(
        {
            "staged_path": "/nonexistent/path.fits",
            "campaign_comp_stars": [{"id": "c1", "x": 5, "y": 5}],
        }
    )
    assert result["status"] == "error"
    assert "error" in result


# ── measurement / role handling ─────────────────────────────────────────


def test_comp_star_only_computes_inst_mag(tmp_path):
    path = tmp_path / "frame.fits"
    data = _flat_frame(level=10.0)
    data[25, 25] = 5000.0  # bright source
    _write_fits(path, data)

    result = lp.run_lp(
        {
            "staged_path": str(path),
            "campaign_comp_stars": [{"id": "comp-a", "x": 25, "y": 25, "role": "comp"}],
        }
    )
    assert result["status"] == "ok"
    assert result["n_comp_stars"] == 1
    assert "lp.inst_mag" in result
    assert result["lp.comp_id"] == "comp-a"
    assert "lp.check_id" not in result


def test_comp_and_check_star_computes_check_minus_comp(tmp_path):
    path = tmp_path / "frame.fits"
    data = _flat_frame(level=10.0)
    data[10, 10] = 5000.0
    data[40, 40] = 2000.0
    _write_fits(path, data)

    result = lp.run_lp(
        {
            "staged_path": str(path),
            "campaign_comp_stars": [
                {"id": "comp-a", "x": 10, "y": 10, "role": "comp"},
                {"id": "check-b", "x": 40, "y": 40, "role": "check"},
            ],
        }
    )
    assert "lp.check_id" in result
    assert "lp.check_minus_comp" in result
    assert result["lp.check_id"] == "check-b"


def test_star_off_frame_edge_uses_flux_floor(tmp_path):
    path = tmp_path / "frame.fits"
    _write_fits(path, _flat_frame())

    result = lp.run_lp(
        {
            "staged_path": str(path),
            "campaign_comp_stars": [{"id": "edge", "x": -5, "y": -5, "role": "comp"}],
        }
    )
    assert result["status"] == "ok"
    # flux floors to 1.0 -> inst_mag = -2.5*log10(1.0) = -0.0
    assert result["lp.inst_mag"] == 0.0 or result["lp.inst_mag"] == -0.0


def test_star_beyond_frame_bounds_uses_flux_floor(tmp_path):
    path = tmp_path / "frame.fits"
    _write_fits(path, _flat_frame(shape=(20, 20)))

    result = lp.run_lp(
        {
            "staged_path": str(path),
            "campaign_comp_stars": [{"id": "far", "x": 500, "y": 500, "role": "comp"}],
        }
    )
    assert result["status"] == "ok"


def test_zero_flux_source_floored_to_one(tmp_path):
    """Uniform background with no source: aperture sum floors to >=1.0 per pixel."""
    path = tmp_path / "frame.fits"
    _write_fits(path, _flat_frame(level=0.0))

    result = lp.run_lp(
        {
            "staged_path": str(path),
            "campaign_comp_stars": [{"id": "c1", "x": 10, "y": 10, "role": "comp"}],
        }
    )
    assert result["status"] == "ok"
    assert result["lp.inst_mag"] is not None


def test_default_role_normalizes_unknown_to_comp(tmp_path):
    path = tmp_path / "frame.fits"
    _write_fits(path, _flat_frame())
    result = lp.run_lp(
        {
            "staged_path": str(path),
            "campaign_comp_stars": [{"id": "c1", "x": 5, "y": 5, "role": "not-a-real-role"}],
        }
    )
    assert result["n_comp_stars"] == 1
    assert "lp.comp_id" in result  # treated as comp


def test_task_snapshot_comp_stars_fallback(tmp_path):
    path = tmp_path / "frame.fits"
    _write_fits(path, _flat_frame())
    result = lp.run_lp(
        {
            "staged_path": str(path),
            "task_snapshot": {"comp_stars": [{"id": "c1", "x": 5, "y": 5}]},
        }
    )
    assert result["n_comp_stars"] == 1


def test_ensemble_weight_uses_default_error_when_ref_err_missing(tmp_path):
    path = tmp_path / "frame.fits"
    _write_fits(path, _flat_frame())
    result = lp.run_lp(
        {
            "staged_path": str(path),
            "campaign_comp_stars": [{"id": "c1", "x": 5, "y": 5, "role": "comp"}],
        }
    )
    assert result["lp.ensemble_weight"] == 1.0  # single comp star -> full weight


def test_ensemble_weight_invalid_ref_err_falls_back(tmp_path):
    path = tmp_path / "frame.fits"
    _write_fits(path, _flat_frame())
    result = lp.run_lp(
        {
            "staged_path": str(path),
            "campaign_comp_stars": [
                {"id": "c1", "x": 5, "y": 5, "role": "comp", "ref_err": "not-a-number"}
            ],
        }
    )
    assert result["lp.ensemble_weight"] == 1.0


def test_aperture_correction_zero_without_fwhm(tmp_path):
    path = tmp_path / "frame.fits"
    _write_fits(path, _flat_frame())
    result = lp.run_lp(
        {
            "staged_path": str(path),
            "campaign_comp_stars": [{"id": "c1", "x": 5, "y": 5, "role": "comp"}],
        }
    )
    assert result["lp.aperture_correction"] == 0.0


def test_aperture_correction_positive_with_fwhm(tmp_path):
    path = tmp_path / "frame.fits"
    _write_fits(path, _flat_frame())
    result = lp.run_lp(
        {
            "staged_path": str(path),
            "campaign_comp_stars": [{"id": "c1", "x": 5, "y": 5, "role": "comp"}],
            "fwhm_px": 3.0,
        }
    )
    assert result["lp.aperture_correction"] > 0.0


def test_sp_fwhm_alias_used_when_fwhm_px_absent(tmp_path):
    path = tmp_path / "frame.fits"
    _write_fits(path, _flat_frame())
    result = lp.run_lp(
        {
            "staged_path": str(path),
            "campaign_comp_stars": [{"id": "c1", "x": 5, "y": 5, "role": "comp"}],
            "sp_fwhm": 3.0,
        }
    )
    assert result["lp.aperture_correction"] > 0.0


def test_invalid_fwhm_value_ignored(tmp_path):
    path = tmp_path / "frame.fits"
    _write_fits(path, _flat_frame())
    result = lp.run_lp(
        {
            "staged_path": str(path),
            "campaign_comp_stars": [{"id": "c1", "x": 5, "y": 5, "role": "comp"}],
            "fwhm_px": "garbage",
        }
    )
    assert result["lp.aperture_correction"] == 0.0


# ── transform integration (#419) ─────────────────────────────────────────


def test_transform_profile_applied_when_color_index_present(tmp_path):
    path = tmp_path / "frame.fits"
    data = _flat_frame(level=10.0)
    data[25, 25] = 5000.0
    _write_fits(path, data)
    profile = transform.load_profile("pier_a_v")

    result = lp.run_lp(
        {
            "staged_path": str(path),
            "campaign_comp_stars": [{"id": "c1", "x": 25, "y": 25, "role": "comp"}],
            "transform_profile": profile,
            "color_index": 0.8,
        }
    )
    assert result["lp.transform_applied"] is True
    assert "lp.std_mag" in result


def test_transform_profile_as_string_loads_by_name(tmp_path):
    path = tmp_path / "frame.fits"
    data = _flat_frame(level=10.0)
    data[10, 10] = 5000.0
    _write_fits(path, data)

    result = lp.run_lp(
        {
            "staged_path": str(path),
            "campaign_comp_stars": [{"id": "c1", "x": 10, "y": 10, "role": "comp"}],
            "transform_profile": "pier_b_v",
            "color_index": 0.5,
        }
    )
    assert result["lp.transform_applied"] is True


def test_load_data_missing_astropy_raises_importerror(monkeypatch):
    monkeypatch.setattr(lp, "fits", None)
    with pytest.raises(ImportError, match="astropy required"):
        lp._load_data("/tmp/whatever.fits")


def test_table_row_build_failure_is_logged_and_swallowed(tmp_path, monkeypatch):
    path = tmp_path / "frame.fits"
    data = _flat_frame(level=10.0)
    data[5, 5] = 5000.0
    _write_fits(path, data)

    def _raise(*a, **k):
        raise RuntimeError("table build exploded")

    monkeypatch.setattr("photometry.table.row_from_lp", _raise)

    result = lp.run_lp(
        {
            "staged_path": str(path),
            "campaign_comp_stars": [{"id": "c1", "x": 5, "y": 5, "role": "comp"}],
        }
    )
    # The table-row build failure is caught locally and does not fail the
    # overall LP result.
    assert result["status"] == "ok"
    assert "photometry_table_row" not in result


def test_no_transform_when_color_index_absent(tmp_path):
    path = tmp_path / "frame.fits"
    data = _flat_frame(level=10.0)
    data[10, 10] = 5000.0
    _write_fits(path, data)
    profile = transform.load_profile("pier_a_v")

    result = lp.run_lp(
        {
            "staged_path": str(path),
            "campaign_comp_stars": [{"id": "c1", "x": 10, "y": 10, "role": "comp"}],
            "transform_profile": profile,
            # color_index missing -> transform not applied
        }
    )
    assert "lp.transform_applied" not in result
