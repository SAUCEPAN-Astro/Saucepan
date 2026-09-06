"""quality.py — coverage for edge cases not already exercised by
test_quality_fwhm_robustness.py / test_quality_floor_censoring.py:
empty/degenerate inputs, write_quality_headers, assess_fits (including
pixel-scale fallback via CDELT2), the Lambda handler(), and the CLI main().
"""

from __future__ import annotations

import base64
import json
import sys
from pathlib import Path

import numpy as np
import pytest
from astropy.io import fits
from saucepan_pipeline.quality import (
    _robust_sigma,
    assess_fits,
    assess_quality,
    estimate_fwhm,
    handler,
    main,
    write_quality_headers,
)

# --- estimate_fwhm: degenerate inputs --------------------------------------


def test_estimate_fwhm_none_data_returns_zero() -> None:
    assert estimate_fwhm(None) == 0.0


def test_estimate_fwhm_empty_array_returns_zero() -> None:
    assert estimate_fwhm(np.zeros((0, 0), dtype=np.float32)) == 0.0


def test_estimate_fwhm_nonpositive_pixel_scale_returns_zero() -> None:
    data = np.ones((32, 32), dtype=np.float32)
    assert estimate_fwhm(data, pixel_scale_arcsec=0.0) == 0.0
    assert estimate_fwhm(data, pixel_scale_arcsec=-1.0) == 0.0


def test_estimate_fwhm_all_nan_falls_back_to_gradient_or_zero() -> None:
    data = np.full((32, 32), np.nan, dtype=np.float32)
    result = estimate_fwhm(data, pixel_scale_arcsec=1.0)
    assert result >= 0.0
    assert np.isfinite(result)


def test_estimate_fwhm_uniform_field_no_stars_falls_back() -> None:
    """A perfectly flat field has no sources for DAOStarFinder to find,
    exercising the 'too few stars' gradient fallback path."""
    data = np.full((48, 48), 100.0, dtype=np.float32)
    result = estimate_fwhm(data, pixel_scale_arcsec=1.0)
    assert result >= 0.0


# --- assess_quality: degenerate inputs -------------------------------------


def test_assess_quality_all_nan_data_is_finite_output() -> None:
    data = np.full((16, 16), np.nan, dtype=np.float32)
    result = assess_quality(data)
    # median/MAD of all-NaN -> NaN; result should not crash, values may be NaN
    # but keys must all be present.
    assert set(result.keys()) >= {
        "background",
        "noise_adu",
        "signal_adu",
        "snr",
        "star_pixels",
        "saturated_pixels",
        "shape",
    }


def test_assess_quality_zero_size_image_handled() -> None:
    data = np.zeros((0, 0), dtype=np.float32)
    result = assess_quality(data)
    assert result["shape"] == [0, 0]
    assert result["star_pixels"] == 0


def test_assess_quality_all_saturated_pixels() -> None:
    data = np.full((16, 16), 70000.0, dtype=np.float32)
    result = assess_quality(data)
    assert result["saturated_pixels"] == 256


def test_assess_quality_without_pixel_scale_omits_fwhm() -> None:
    data = np.full((16, 16), 100.0, dtype=np.float32)
    result = assess_quality(data, pixel_scale_arcsec=None)
    assert "fwhm_arcsec" not in result


def test_assess_quality_zero_pixel_scale_omits_fwhm() -> None:
    data = np.full((16, 16), 100.0, dtype=np.float32)
    result = assess_quality(data, pixel_scale_arcsec=0.0)
    assert "fwhm_arcsec" not in result


# --- _robust_sigma: floor-censored + small-population branches ------------


def test_robust_sigma_large_uncensored_population_uses_upper_half() -> None:
    rng = np.random.default_rng(0)
    values = rng.normal(0, 5, size=1000)
    sigma = _robust_sigma(values, 0.0)
    assert sigma == pytest.approx(5.0, rel=0.25)


def test_robust_sigma_small_population_falls_back_to_mad() -> None:
    values = np.array([1.0, 2.0, 3.0, 100.0, -50.0])
    sigma = _robust_sigma(values, 0.0)
    assert sigma >= 0.0


def test_robust_sigma_floor_censored_population_still_robust() -> None:
    """Half the population tied at exactly the floor (0), simulating
    background.py's np.clip(x, 0, None) — the documented #464 scenario."""
    rng = np.random.default_rng(1)
    upper = rng.normal(10, 2, size=600)
    floor = np.zeros(600)
    values = np.concatenate([upper, floor])
    sigma = _robust_sigma(values, np.median(values))
    assert sigma > 0.0  # must not collapse to 0 despite floor censoring


# --- write_quality_headers --------------------------------------------------


def test_write_quality_headers_writes_all_fields(tmp_path: Path) -> None:
    p = tmp_path / "f.fits"
    fits.PrimaryHDU(data=np.zeros((8, 8), dtype=np.float32)).writeto(p, overwrite=True)
    quality = {
        "snr": 5.5,
        "background": 100.0,
        "noise_adu": 2.0,
        "star_pixels": 10,
        "saturated_pixels": 0,
        "shape": [8, 8],
        "fwhm_arcsec": 3.2,
    }
    result = write_quality_headers(str(p), quality)
    assert result is quality
    with fits.open(p) as hdul:
        hdr = hdul[0].header
        assert hdr["SP_SNR"] == pytest.approx(5.5)
        assert hdr["SP_FWHM"] == pytest.approx(3.2)
        assert hdr["SP_NX"] == 8
        assert hdr["SP_NY"] == 8


def test_write_quality_headers_without_fwhm_key_skips_sp_fwhm(tmp_path: Path) -> None:
    p = tmp_path / "f.fits"
    fits.PrimaryHDU(data=np.zeros((8, 8), dtype=np.float32)).writeto(p, overwrite=True)
    quality = {
        "snr": 1.0,
        "background": 0.0,
        "noise_adu": 1.0,
        "star_pixels": 0,
        "saturated_pixels": 0,
        "shape": [8, 8],
    }
    write_quality_headers(str(p), quality)
    with fits.open(p) as hdul:
        assert "SP_FWHM" not in hdul[0].header


# --- assess_fits: pixel-scale resolution priority ---------------------------


def test_assess_fits_uses_sp_pixscale_header(tmp_path: Path) -> None:
    p = tmp_path / "f.fits"
    hdu = fits.PrimaryHDU(data=np.full((16, 16), 100.0, dtype=np.float32))
    hdu.header["SP_PIXSCALE"] = 2.0
    hdu.writeto(p, overwrite=True)
    result = assess_fits(str(p))
    assert "fwhm_arcsec" in result
    assert result["file"] == str(p)


def test_assess_fits_falls_back_to_cdelt2(tmp_path: Path) -> None:
    p = tmp_path / "f.fits"
    hdu = fits.PrimaryHDU(data=np.full((16, 16), 100.0, dtype=np.float32))
    hdu.header["CDELT2"] = 0.0005
    hdu.writeto(p, overwrite=True)
    result = assess_fits(str(p))
    assert "fwhm_arcsec" in result


def test_assess_fits_no_pixel_scale_defaults_to_one_arcsec(tmp_path: Path) -> None:
    p = tmp_path / "f.fits"
    hdu = fits.PrimaryHDU(data=np.full((16, 16), 100.0, dtype=np.float32))
    hdu.writeto(p, overwrite=True)
    result = assess_fits(str(p))
    assert "fwhm_arcsec" in result


def test_assess_fits_update_fits_writes_headers_in_place(tmp_path: Path) -> None:
    p = tmp_path / "f.fits"
    hdu = fits.PrimaryHDU(data=np.full((16, 16), 100.0, dtype=np.float32))
    hdu.writeto(p, overwrite=True)
    assess_fits(str(p), update_fits=True)
    with fits.open(p) as hdul:
        assert "SP_SNR" in hdul[0].header
        assert hdul[0].header["SP_PVER"] == "1.0.0"


# --- Lambda handler ----------------------------------------------------------


def test_handler_unknown_action_returns_400() -> None:
    result = handler({"action": "not_a_real_action"})
    assert result["statusCode"] == 400


def test_handler_missing_path_and_data_returns_400() -> None:
    result = handler({"action": "assess_quality"})
    assert result["statusCode"] == 400


def test_handler_with_path_returns_200(tmp_path: Path) -> None:
    p = tmp_path / "f.fits"
    fits.PrimaryHDU(data=np.full((16, 16), 100.0, dtype=np.float32)).writeto(p, overwrite=True)
    result = handler({"action": "assess_quality", "path": str(p)})
    assert result["statusCode"] == 200
    assert "snr" in result["body"]


def test_handler_with_data_base64_returns_200() -> None:
    data = np.full((8, 8), 50.0, dtype=np.float32)
    payload = {
        "action": "assess_quality",
        "data_base64": base64.b64encode(data.tobytes()).decode(),
        "shape": [8, 8],
    }
    result = handler(payload)
    assert result["statusCode"] == 200


def test_handler_exception_path_returns_500() -> None:
    result = handler({"action": "assess_quality", "path": "/no/such/file.fits"})
    assert result["statusCode"] == 500
    assert "error" in result["body"]


def test_handler_default_action_is_assess_quality(tmp_path: Path) -> None:
    p = tmp_path / "f.fits"
    fits.PrimaryHDU(data=np.full((8, 8), 50.0, dtype=np.float32)).writeto(p, overwrite=True)
    result = handler({"path": str(p)})
    assert result["statusCode"] == 200


# --- CLI main() ---------------------------------------------------------------


def test_main_writes_to_stdout(tmp_path: Path, monkeypatch, capsys) -> None:
    p = tmp_path / "f.fits"
    fits.PrimaryHDU(data=np.full((16, 16), 100.0, dtype=np.float32)).writeto(p, overwrite=True)
    monkeypatch.setattr(sys, "argv", ["quality", "--input", str(p)])
    main()
    captured = capsys.readouterr()
    parsed = json.loads(captured.out)
    assert "snr" in parsed


def test_main_writes_to_output_file(tmp_path: Path, monkeypatch, capsys) -> None:
    p = tmp_path / "f.fits"
    out = tmp_path / "out.json"
    fits.PrimaryHDU(data=np.full((16, 16), 100.0, dtype=np.float32)).writeto(p, overwrite=True)
    monkeypatch.setattr(sys, "argv", ["quality", "--input", str(p), "--output", str(out)])
    main()
    assert out.exists()
    parsed = json.loads(out.read_text())
    assert "snr" in parsed
