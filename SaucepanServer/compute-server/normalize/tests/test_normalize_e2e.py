"""normalize.normalize_fits() end-to-end — camera synonym mapping, WCS
extraction, pixel-scale derivation, tier computation, and the SP_ header
contract (HDU[0]=SP_ canonical, HDU[1]=ORIGHDRS original headers verbatim).
"""

from __future__ import annotations

import sys
from pathlib import Path

import numpy as np
import pytest
from astropy.io import fits

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))

from normalize.normalize import (
    NormalizationResult,
    _compute_pixel_scale,
    _extract_coords_wcs,
    normalize_fits,
)


def _write_fits(path: Path, header_kv: dict, *, naxis1=16, naxis2=16) -> None:
    data = np.ones((naxis2, naxis1), dtype=np.float32) * 100.0
    hdu = fits.PrimaryHDU(data=data)
    for k, v in header_kv.items():
        hdu.header[k] = v
    hdu.writeto(path, overwrite=True)


# --- full mandatory set -> tier 1, ORIGHDRS preserved -------------------


def test_normalize_fits_tier1_with_all_mandatory_headers(tmp_path: Path) -> None:
    inp = tmp_path / "in.fits"
    out = tmp_path / "out.fits"
    _write_fits(
        inp,
        {
            "RA": 180.0,
            "DEC": 0.0,
            "TELESCOP": "test-scope",
            "FILTER": "V",
            "EXPTIME": 30.0,
            "DATE-OBS": "2024-01-01T00:00:00",
            "INSTRUME": "TestCam",
        },
    )
    result = normalize_fits(str(inp), str(out), source="live")
    assert result.success
    assert result.tier == 1
    assert set(["SP_RA", "SP_DEC", "SP_TELE", "SP_FILTER", "SP_EXPTIME", "SP_DATEOBS"]) <= set(
        result.resolved
    )
    assert result.unresolved == []

    with fits.open(out) as hdul:
        assert hdul[0].header["SP_TELE"] == "test-scope"
        assert hdul[0].header["SP_TIER"] == 1
        assert hdul[1].header["EXTNAME"] == "ORIGHDRS"
        # Original headers preserved verbatim in HDU[1] table.
        orig_keys = list(hdul[1].data["KEYWORD"])
        assert b"TELESCOP" in orig_keys or "TELESCOP" in orig_keys


def test_normalize_fits_tier3_with_no_mandatory_headers(tmp_path: Path) -> None:
    inp = tmp_path / "in.fits"
    out = tmp_path / "out.fits"
    _write_fits(inp, {"SOMEJUNK": "value"})
    result = normalize_fits(str(inp), str(out))
    assert result.success
    assert result.tier == 3
    assert len(result.unresolved) == 6  # all mandatory missing


def test_normalize_fits_camera_synonym_new_camera_via_extra_vocab(tmp_path: Path) -> None:
    """Demonstrates the documented extension path: a new camera's headers
    map via an extra synonyms YAML, no pipeline code touched."""
    inp = tmp_path / "in.fits"
    out = tmp_path / "out.fits"
    vocab_path = tmp_path / "extra_vocab.yaml"
    vocab_path.write_text("SP_EXPTIME:\n  synonyms: [WEIRDCAM_EXPOSURE]\n  transform: float\n")
    _write_fits(
        inp,
        {
            "RA": 10.0,
            "DEC": -20.0,
            "TELESCOP": "weird-scope",
            "FILTER": "Ha",
            "WEIRDCAM_EXPOSURE": 5.0,
            "DATE-OBS": "2024-06-01T00:00:00",
        },
    )
    result = normalize_fits(str(inp), str(out), extra_vocab_path=str(vocab_path))
    assert result.success
    assert "SP_EXPTIME" in result.resolved
    with fits.open(out) as hdul:
        assert hdul[0].header["SP_EXPTIME"] == 5.0
        assert hdul[0].header["SP_FILTER"] == "Ha"


# --- WCS coordinate extraction -------------------------------------------


def test_normalize_fits_uses_wcs_when_ra_dec_absent(tmp_path: Path) -> None:
    inp = tmp_path / "in.fits"
    out = tmp_path / "out.fits"
    _write_fits(
        inp,
        {
            "TELESCOP": "wcs-scope",
            "FILTER": "V",
            "EXPTIME": 10.0,
            "DATE-OBS": "2024-01-01T00:00:00",
            "CTYPE1": "RA---TAN",
            "CTYPE2": "DEC--TAN",
            "CRPIX1": 8.0,
            "CRPIX2": 8.0,
            "CRVAL1": 45.0,
            "CRVAL2": 10.0,
            "CDELT1": -0.001,
            "CDELT2": 0.001,
        },
    )
    result = normalize_fits(str(inp), str(out))
    assert result.wcs_used is True
    assert "SP_RA" in result.resolved
    assert "SP_DEC" in result.resolved


def test_extract_coords_wcs_returns_none_without_celestial_wcs() -> None:
    header = fits.Header()
    header["NAXIS1"] = 10
    header["NAXIS2"] = 10
    ra, dec, used = _extract_coords_wcs(header, np.zeros((10, 10)))
    assert ra is None and dec is None and used is False


# --- pixel scale computation ----------------------------------------------


def test_compute_pixel_scale_from_optics() -> None:
    # pixel_size_um / focal_length_mm * 206.265
    ps = _compute_pixel_scale({"XPIXSZ": 3.76, "FOCALLEN": 600.0})
    assert ps == pytest.approx((3.76 / 600.0) * 206.265, rel=1e-3)


def test_compute_pixel_scale_from_cdelt_fallback() -> None:
    ps = _compute_pixel_scale({"CDELT2": 0.0005})
    assert ps == pytest.approx(0.0005 * 3600.0, rel=1e-6)


def test_compute_pixel_scale_none_when_no_data() -> None:
    assert _compute_pixel_scale({}) is None


def test_compute_pixel_scale_ignores_malformed_optics_falls_to_cdelt() -> None:
    ps = _compute_pixel_scale({"XPIXSZ": "garbage", "FOCALLEN": 600.0, "CDELT2": 0.001})
    assert ps == pytest.approx(3.6, rel=1e-6)


def test_normalize_fits_computes_pixscale_from_optics(tmp_path: Path) -> None:
    inp = tmp_path / "in.fits"
    out = tmp_path / "out.fits"
    _write_fits(
        inp,
        {
            "RA": 1.0,
            "DEC": 1.0,
            "TELESCOP": "t",
            "FILTER": "V",
            "EXPTIME": 1.0,
            "DATE-OBS": "2024-01-01T00:00:00",
            "XPIXSZ": 3.76,
            "FOCALLEN": 600.0,
        },
    )
    result = normalize_fits(str(inp), str(out))
    assert "SP_PIXSCALE" in result.resolved
    with fits.open(out) as hdul:
        assert hdul[0].header["SP_PIXSCALE"] == pytest.approx((3.76 / 600.0) * 206.265, rel=1e-3)


# --- malformed / missing / empty edge cases -------------------------------


def test_normalize_fits_missing_input_file_reports_error(tmp_path: Path) -> None:
    result = normalize_fits(str(tmp_path / "nope.fits"), str(tmp_path / "out.fits"))
    assert result.success is False
    assert result.error is not None
    assert "Cannot open FITS" in result.error


def test_normalize_fits_empty_image_still_normalizes(tmp_path: Path) -> None:
    inp = tmp_path / "in.fits"
    out = tmp_path / "out.fits"
    data = np.zeros((0, 0), dtype=np.float32)
    hdu = fits.PrimaryHDU(data=data)
    hdu.header["RA"] = 1.0
    hdu.header["DEC"] = 1.0
    hdu.header["TELESCOP"] = "t"
    hdu.header["FILTER"] = "V"
    hdu.header["EXPTIME"] = 1.0
    hdu.header["DATE-OBS"] = "2024-01-01T00:00:00"
    hdu.writeto(inp, overwrite=True)
    result = normalize_fits(str(inp), str(out))
    assert result.success is True


def test_normalize_fits_malformed_date_obs_leaves_dateobs_unresolved(tmp_path: Path) -> None:
    inp = tmp_path / "in.fits"
    out = tmp_path / "out.fits"
    _write_fits(
        inp,
        {
            "RA": 1.0,
            "DEC": 1.0,
            "TELESCOP": "t",
            "FILTER": "V",
            "EXPTIME": 1.0,
            "DATE-OBS": "not-a-real-date-at-all",
        },
    )
    result = normalize_fits(str(inp), str(out))
    assert result.success is True
    assert "SP_DATEOBS" not in result.resolved
    assert "SP_DATEOBS" in result.unresolved


def test_normalize_fits_respects_existing_sp_pixscale_header(tmp_path: Path) -> None:
    """A file that already carries SP_PIXSCALE (e.g. re-normalized input)
    should use it verbatim rather than recomputing from optics/CDELT."""
    inp = tmp_path / "in.fits"
    out = tmp_path / "out.fits"
    _write_fits(
        inp,
        {
            "RA": 1.0,
            "DEC": 1.0,
            "TELESCOP": "t",
            "FILTER": "V",
            "EXPTIME": 1.0,
            "DATE-OBS": "2024-01-01T00:00:00",
            "SP_PIXSCALE": 1.23,
        },
    )
    result = normalize_fits(str(inp), str(out))
    with fits.open(out) as hdul:
        assert hdul[0].header["SP_PIXSCALE"] == pytest.approx(1.23)


def test_normalize_fits_writes_sp_prov_when_metrics_env_enabled(
    tmp_path: Path, monkeypatch
) -> None:
    monkeypatch.setenv("METRICS_PROV_URI", "1")
    inp = tmp_path / "in.fits"
    out = tmp_path / "out.fits"
    _write_fits(
        inp,
        {
            "RA": 1.0,
            "DEC": 1.0,
            "TELESCOP": "t",
            "FILTER": "V",
            "EXPTIME": 1.0,
            "DATE-OBS": "2024-01-01T00:00:00",
        },
    )
    result = normalize_fits(str(inp), str(out), source="live")
    assert "SP_PROV" in result.resolved
    with fits.open(out) as hdul:
        assert "SP_PROV" in hdul[0].header


def test_normalize_fits_no_sp_prov_when_metrics_env_disabled(tmp_path: Path, monkeypatch) -> None:
    monkeypatch.delenv("METRICS_PROV_URI", raising=False)
    inp = tmp_path / "in.fits"
    out = tmp_path / "out.fits"
    _write_fits(
        inp,
        {
            "RA": 1.0,
            "DEC": 1.0,
            "TELESCOP": "t",
            "FILTER": "V",
            "EXPTIME": 1.0,
            "DATE-OBS": "2024-01-01T00:00:00",
        },
    )
    result = normalize_fits(str(inp), str(out))
    assert "SP_PROV" not in result.resolved


def test_normalize_main_cli_writes_json_and_exits_zero(tmp_path: Path, monkeypatch, capsys) -> None:
    import normalize.normalize as normalize_mod

    inp = tmp_path / "in.fits"
    out = tmp_path / "out.fits"
    _write_fits(
        inp,
        {
            "RA": 1.0,
            "DEC": 1.0,
            "TELESCOP": "t",
            "FILTER": "V",
            "EXPTIME": 1.0,
            "DATE-OBS": "2024-01-01T00:00:00",
        },
    )
    monkeypatch.setattr(sys, "argv", ["normalize", str(inp), str(out)])
    with pytest.raises(SystemExit) as excinfo:
        normalize_mod.main()
    assert excinfo.value.code == 0
    captured = capsys.readouterr()
    assert '"success": true' in captured.out


def test_normalize_main_cli_nonzero_exit_on_failure(tmp_path: Path, monkeypatch) -> None:
    import normalize.normalize as normalize_mod

    monkeypatch.setattr(
        sys, "argv", ["normalize", str(tmp_path / "missing.fits"), str(tmp_path / "out.fits")]
    )
    with pytest.raises(SystemExit) as excinfo:
        normalize_mod.main()
    assert excinfo.value.code == 1


def test_result_to_dict_round_trips_fields() -> None:
    r = NormalizationResult(
        success=True,
        tier=1,
        resolved=["SP_RA"],
        unresolved=[],
        output_path="/x",
        wcs_used=True,
        error=None,
    )
    d = r.to_dict()
    assert d["tier"] == 1
    assert d["wcs_used"] is True
    assert d["resolved"] == ["SP_RA"]
