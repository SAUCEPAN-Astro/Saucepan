"""Edge-case coverage for photometry.pipeline (detect -> plate solve -> ZP -> depth).

Exercises the pure/private helpers directly (checksum, WCS presence, ZP fit,
depth, phot_flag) plus run_photometry's fail-open behavior end to end, since
those are the branches with the least existing coverage.
"""

from __future__ import annotations

import math

import numpy as np
import pytest
from astropy.io import fits
from photometry import pipeline, platesolve_cache


def setup_function() -> None:
    platesolve_cache.clear()


def _write_fits(path, data: np.ndarray, header: dict | None = None) -> None:
    hdu = fits.PrimaryHDU(data=data.astype(np.float32))
    for key, value in (header or {}).items():
        hdu.header[key] = value
    hdu.writeto(path, overwrite=True)


# ── run_photometry: fail-open + success smoke ───────────────────────────


def test_run_photometry_missing_file_fails_open(tmp_path):
    summary = pipeline.run_photometry(str(tmp_path / "missing.fits"), {})
    assert summary["status"] == "error"
    assert summary["phot_flag"] == 4
    assert "error" in summary


def test_run_photometry_success_smoke_without_fits_update(tmp_path):
    path = tmp_path / "frame.fits"
    rng = np.random.default_rng(0)
    data = rng.normal(loc=100.0, scale=5.0, size=(60, 60)).astype(np.float32)
    data[30, 30] = 5000.0  # one bright source
    _write_fits(
        path,
        data,
        {
            "SP_RA": 10.0,
            "SP_DEC": 20.0,
            "SP_EXPTIME": 30.0,
        },
    )
    summary = pipeline.run_photometry(str(path), {"upload_id": "u1"}, update_fits=False)
    assert summary["status"] == "ok"
    assert summary["upload_id"] == "u1"
    assert "n_sources" in summary
    assert "phot_flag" in summary


def test_run_photometry_writes_headers_when_update_fits_true(tmp_path):
    path = tmp_path / "frame.fits"
    rng = np.random.default_rng(1)
    data = rng.normal(loc=100.0, scale=5.0, size=(60, 60)).astype(np.float32)
    data[10, 10] = 8000.0
    _write_fits(path, data, {"SP_RA": 5.0, "SP_DEC": 5.0, "SP_EXPTIME": 20.0})

    summary = pipeline.run_photometry(str(path), {}, update_fits=True)
    assert summary["status"] == "ok"

    with fits.open(path) as hdul:
        hdr = hdul[0].header
        assert "SP_PHOTFLAG" in hdr


def test_load_image_missing_astropy_raises_importerror(monkeypatch):
    monkeypatch.setattr(pipeline, "fits", None)
    with pytest.raises(ImportError, match="astropy required"):
        pipeline._load_image("/tmp/whatever.fits")


def test_run_photometry_missing_astropy_fails_open(tmp_path, monkeypatch):
    monkeypatch.setattr(pipeline, "fits", None)
    path = tmp_path / "frame.fits"
    _write_fits(path, np.zeros((5, 5), dtype=np.float32))
    summary = pipeline.run_photometry(str(path), {})
    assert summary["status"] == "error"
    assert "astropy required" in summary["error"]


def test_run_photometry_upload_id_passthrough_missing_ctx(tmp_path):
    path = tmp_path / "frame.fits"
    _write_fits(path, np.zeros((10, 10), dtype=np.float32))
    summary = pipeline.run_photometry(str(path), {})
    assert summary["upload_id"] is None


# ── _frame_checksum ───────────────────────────────────────────────────────


def test_frame_checksum_prefers_sp_chksum_header():
    hdr = {"SP_CHKSUM": " abc123 "}
    assert pipeline._frame_checksum(hdr, {}) == "abc123"


def test_frame_checksum_falls_back_to_checksum_header():
    hdr = {"CHECKSUM": "def456"}
    assert pipeline._frame_checksum(hdr, {}) == "def456"


def test_frame_checksum_falls_back_to_catalog_ctx():
    hdr = {}
    ctx = {"_catalog": {"checksum_sha256": "cat-checksum"}}
    assert pipeline._frame_checksum(hdr, ctx) == "cat-checksum"


def test_frame_checksum_falls_back_to_ctx_checksum_sha256():
    hdr = {}
    ctx = {"checksum_sha256": "ctx-checksum"}
    assert pipeline._frame_checksum(hdr, ctx) == "ctx-checksum"


def test_frame_checksum_none_when_nothing_available():
    assert pipeline._frame_checksum({}, {}) is None


def test_frame_checksum_blank_header_value_skipped():
    hdr = {"SP_CHKSUM": "   ", "CHECKSUM": "real"}
    assert pipeline._frame_checksum(hdr, {}) == "real"


# ── _wcs_present ───────────────────────────────────────────────────────


def test_wcs_present_all_keys():
    hdr = {"CTYPE1": "RA---TAN", "CRVAL1": 10.0, "CRPIX1": 512.0}
    assert pipeline._wcs_present(hdr) is True


def test_wcs_present_missing_one_key():
    hdr = {"CTYPE1": "RA---TAN", "CRVAL1": 10.0}
    assert pipeline._wcs_present(hdr) is False


def test_wcs_present_empty_header():
    assert pipeline._wcs_present({}) is False


# ── _fwhm_pixels ───────────────────────────────────────────────────────


def test_fwhm_pixels_default_when_no_header():
    assert pipeline._fwhm_pixels({}) == 3.0


def test_fwhm_pixels_from_sp_fwhm_with_pixscale():
    hdr = {"SP_FWHM": 4.0, "SP_PIXSCALE": 2.0}
    assert pipeline._fwhm_pixels(hdr) == 2.0


def test_fwhm_pixels_clamped_to_minimum_two():
    hdr = {"SP_FWHM": 0.1, "SP_PIXSCALE": 1.0}
    assert pipeline._fwhm_pixels(hdr) == 2.0


def test_fwhm_pixels_seeing_alias():
    hdr = {"SEEING": 6.0, "PIXSCALE": 3.0}
    assert pipeline._fwhm_pixels(hdr) == 2.0


def test_fwhm_pixels_invalid_value_falls_back_to_default():
    hdr = {"SP_FWHM": "not-a-number"}
    assert pipeline._fwhm_pixels(hdr) == 3.0


def test_fwhm_pixels_explicit_zero_pixscale_is_rejected_not_defaulted():
    # #486: an explicitly supplied SP_PIXSCALE=0.0 is invalid input, not a
    # missing value. The `is None` chain keeps the real 0.0, which then fails
    # the `pixscale > 0` guard, so _fwhm_pixels falls through to its default
    # FWHM (3.0) instead of silently pretending the scale was 1.0 arcsec/px.
    hdr = {"SP_FWHM": 4.0, "SP_PIXSCALE": 0.0}
    assert pipeline._fwhm_pixels(hdr) == 3.0


def test_fwhm_pixels_explicit_negative_pixscale_is_rejected_not_defaulted():
    hdr = {"SP_FWHM": 4.0, "SP_PIXSCALE": -1.5}
    assert pipeline._fwhm_pixels(hdr) == 3.0


# ── _stub_plate_solve / _plate_solve ────────────────────────────────────


def test_stub_plate_solve_ok_with_ra_dec_and_enough_sources():
    hdr = {"SP_RA": 10.0, "SP_DEC": 20.0}
    sources = {"flux": [1.0] * 10}
    result = pipeline._stub_plate_solve(hdr, sources)
    assert result["ok"] is True
    assert result["ra"] == 10.0


def test_stub_plate_solve_fails_without_ra_dec():
    result = pipeline._stub_plate_solve({}, {"flux": [1.0] * 10})
    assert result["ok"] is False
    assert result["ra"] is None


def test_stub_plate_solve_fails_with_too_few_sources():
    hdr = {"SP_RA": 10.0, "SP_DEC": 20.0}
    result = pipeline._stub_plate_solve(hdr, {"flux": [1.0, 2.0]})
    assert result["ok"] is False


def test_plate_solve_uses_existing_wcs_when_present():
    hdr = {"CTYPE1": "RA---TAN", "CRVAL1": 10.0, "CRPIX1": 512.0, "CRVAL2": 20.0}
    result = pipeline._plate_solve("dummy.fits", hdr, {"flux": []}, None)
    assert result["ok"] is True
    assert result["method"] == "existing_wcs"
    assert result["cached"] is False


def test_plate_solve_cache_hit_returns_cached_true():
    key = platesolve_cache.make_key("upload-x", "chk-1")
    platesolve_cache.put(key, {"ok": True, "method": "stub"})
    result = pipeline._plate_solve("dummy.fits", {}, {"flux": []}, key)
    assert result["cached"] is True


def test_plate_solve_none_key_still_solves_uncached():
    hdr = {"SP_RA": 1.0, "SP_DEC": 2.0}
    sources = {"flux": [1.0] * 10}
    result = pipeline._plate_solve("dummy.fits", hdr, sources, None)
    assert result["cached"] is False


# ── _fit_zeropoint (catalog-matched, fail-closed by default — #203) ────


def test_fit_zeropoint_no_sources_returns_none():
    result = pipeline._fit_zeropoint({"flux": []}, {"ok": True}, {})
    assert result["zp"] is None
    assert result["n_cal_stars"] == 0
    assert result["ok"] is False


def test_fit_zeropoint_plate_not_ok_returns_none():
    result = pipeline._fit_zeropoint({"flux": [100.0]}, {"ok": False}, {})
    assert result["zp"] is None
    assert result["ok"] is False
    assert result["reason"] == "plate_solve_failed"


def test_fit_zeropoint_no_reference_catalog_fails_closed():
    """Default science path: no external reference -> no number, non-photometric."""
    result = pipeline._fit_zeropoint(
        {"flux": [100.0] * 20, "x": list(range(20)), "y": [1.0] * 20},
        {"ok": True, "ra": 10.0, "dec": 20.0},
        {"SP_EXPTIME": 30.0},
    )
    assert result["zp"] is None
    assert result["ok"] is False
    assert result["reason"] in {"no_reference_catalog", "no_wcs_for_match"}


def test_fit_zeropoint_stub_only_behind_env(monkeypatch):
    monkeypatch.setenv("PHOT_STUB", "1")
    result = pipeline._fit_zeropoint({"flux": [100.0]}, {"ok": True}, {"SP_EXPTIME": 30.0})
    assert result["zp"] is not None
    assert result["zp_rms"] == 0.02
    assert result["n_cal_stars"] == 1
    assert result["catalog"] == "stub"


def test_fit_zeropoint_stub_caps_at_thirty_cal_stars(monkeypatch):
    monkeypatch.setenv("PHOT_STUB", "1")
    flux = list(np.linspace(100.0, 500.0, 50))
    result = pipeline._fit_zeropoint({"flux": flux}, {"ok": True}, {})
    assert result["n_cal_stars"] == 30


def test_fit_zeropoint_stub_default_exptime_when_missing(monkeypatch):
    monkeypatch.setenv("PHOT_STUB", "1")
    result = pipeline._fit_zeropoint({"flux": [50.0, 60.0]}, {"ok": True}, {})
    assert result["zp"] is not None


def test_run_photometry_recovers_zp_with_inline_reference_end_to_end(tmp_path):
    """Black-box: real catalog-matched ZP flows through run_photometry and lands
    in the summary + FITS headers (SP_ZP / SP_ZPCAT / SP_PHOTFLG=PHOT)."""
    from astropy.wcs import WCS

    nx = ny = 220
    w = WCS(naxis=2)
    w.wcs.crpix = [nx / 2, ny / 2]
    w.wcs.cdelt = [-1.0 / 3600.0, 1.0 / 3600.0]
    w.wcs.crval = [150.0, 2.0]
    w.wcs.ctype = ["RA---TAN", "DEC--TAN"]

    rng = np.random.default_rng(7)
    data = rng.normal(100.0, 3.0, size=(ny, nx)).astype(np.float32)
    xs = rng.uniform(20, 200, size=18)
    ys = rng.uniform(20, 200, size=18)
    flux = rng.uniform(4_000.0, 90_000.0, size=18)
    for x, y, f in zip(xs, ys, flux):
        data[int(round(y)), int(round(x))] += f

    exptime = 25.0
    zp_true = 24.6
    ra, dec = w.all_pix2world(xs, ys, 0)
    inst = -2.5 * np.log10(flux / exptime)
    refs = [
        {"ra": float(r), "dec": float(d), "mag": float(m + zp_true), "mag_err": 0.01}
        for r, d, m in zip(ra, dec, inst)
    ]

    path = tmp_path / "wcsframe.fits"
    hdu = fits.PrimaryHDU(data=data)
    for k, v in w.to_header().items():
        hdu.header[k] = v
    hdu.header["SP_EXPTIME"] = exptime
    hdu.header["MJD-OBS"] = 60123.25
    hdu.writeto(path, overwrite=True)

    summary = pipeline.run_photometry(
        str(path), {"phot_reference": refs}, update_fits=True
    )
    assert summary["status"] == "ok"
    assert summary["zp_ok"] is True
    assert summary["zp"] == pytest.approx(zp_true, abs=0.05)
    assert summary["zp_epoch"] == 60123.25
    assert (summary["phot_flag"] & 8) == 0

    with fits.open(path) as hdul:
        hdr = hdul[0].header
        assert hdr["SP_PHOTFLG"] == "PHOT"
        assert "SP_ZPCAT" in hdr
        assert hdr["SP_ZP"] == pytest.approx(zp_true, abs=0.05)


def test_run_photometry_non_photometric_when_no_reference(tmp_path):
    path = tmp_path / "frame.fits"
    rng = np.random.default_rng(2)
    data = rng.normal(100.0, 5.0, size=(80, 80)).astype(np.float32)
    for i in range(10):
        data[10 + 5 * i, 10 + 5 * i] = 6000.0
    _write_fits(path, data, {"SP_RA": 5.0, "SP_DEC": 5.0, "SP_EXPTIME": 20.0})

    summary = pipeline.run_photometry(str(path), {}, update_fits=True)
    assert summary["status"] == "ok"
    assert summary["zp"] is None
    assert summary["zp_ok"] is False
    assert summary["phot_flag"] & 8
    with fits.open(path) as hdul:
        assert hdul[0].header["SP_PHOTFLG"] == "NONPHOT"


# ── _measure_depth ────────────────────────────────────────────────────


def test_measure_depth_returns_none_limmag_when_zp_missing():
    data = np.full((10, 10), 5.0, dtype=np.float32)
    depth = pipeline._measure_depth(data, {}, {"zp": None})
    assert depth["limmag_5sigma"] is None
    assert depth["skymag"] is not None


def test_measure_depth_computes_limmag_when_zp_present():
    data = np.full((10, 10), 5.0, dtype=np.float32)
    depth = pipeline._measure_depth(data, {}, {"zp": 25.0})
    assert depth["limmag_5sigma"] is not None


def test_measure_depth_uses_pixscale_header():
    data = np.full((10, 10), 5.0, dtype=np.float32)
    depth_default = pipeline._measure_depth(data, {}, {"zp": 25.0})
    depth_scaled = pipeline._measure_depth(data, {"SP_PIXSCALE": 2.0}, {"zp": 25.0})
    assert depth_default["skymag"] != depth_scaled["skymag"]


def test_measure_depth_perfectly_uniform_frame_yields_finite_limmag():
    """
    #486 regression: for a perfectly uniform frame the inner std is 0.0, so the
    boolean mask ``data < bg`` selects nothing and ``np.std([])`` is NaN. NaN is
    truthy in Python, so the old ``... or 1.0`` fallback never fired and NaN
    leaked through ``sigma_flux = 5.0 * noise`` / ``max(sigma_flux, 1.0)`` into
    ``limmag_5sigma`` and (with update_fits=True) the SP_LIMMAG5 FITS header /
    ``/v1/photometry`` response. _measure_depth now guards the empty-mask and
    non-finite cases, so noise falls back to 1.0 and limmag stays finite.
    """
    data = np.full((10, 10), 5.0, dtype=np.float32)
    depth = pipeline._measure_depth(data, {}, {"zp": 25.0})
    assert depth["limmag_5sigma"] is not None
    assert not math.isnan(depth["limmag_5sigma"])
    # noise defaults to 1.0 -> limmag = zp - 2.5*log10(max(5*1.0, 1.0)) = zp - 1.747
    assert depth["limmag_5sigma"] == pytest.approx(25.0 - 2.5 * math.log10(5.0), abs=1e-3)


# ── _phot_flag ────────────────────────────────────────────────────────


def test_phot_flag_ok_when_plate_ok_and_low_rms():
    flag = pipeline._phot_flag({"zp_rms": 0.01}, {"ok": True})
    assert flag == 0


def test_phot_flag_high_rms_sets_bit_1():
    flag = pipeline._phot_flag({"zp_rms": 0.5}, {"ok": True})
    assert flag & 1


def test_phot_flag_plate_not_ok_sets_bit_2():
    flag = pipeline._phot_flag({"zp_rms": 0.01}, {"ok": False})
    assert flag & 2


def test_phot_flag_both_bits_set():
    flag = pipeline._phot_flag({"zp_rms": 0.5}, {"ok": False})
    assert flag == 3


def test_phot_flag_none_zp_rms_no_bit_1():
    flag = pipeline._phot_flag({"zp_rms": None}, {"ok": True})
    assert flag == 0


# ── _stub_sources ─────────────────────────────────────────────────────


def test_stub_sources_finds_bright_pixels_above_threshold():
    data = np.full((30, 30), 10.0, dtype=np.float32)
    data[15, 15] = 5000.0
    sources = pipeline._stub_sources(data, {})
    assert sources["method"] == "stub"
    assert len(sources["x"]) >= 1


def test_stub_sources_uniform_frame_falls_back_to_center_pixel():
    data = np.full((20, 20), 100.0, dtype=np.float32)  # zero std -> no pixel above threshold
    sources = pipeline._stub_sources(data, {})
    assert len(sources["x"]) == 1
    assert sources["x"][0] == 10.0
    assert sources["y"][0] == 10.0


def test_stub_sources_flux_floored_at_one():
    data = np.full((20, 20), 100.0, dtype=np.float32)
    sources = pipeline._stub_sources(data, {})
    assert all(f >= 1.0 for f in sources["flux"])
