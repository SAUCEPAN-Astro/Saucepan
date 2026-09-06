"""reproject_frame.py (module-level lambda wrapper) — WCS reprojection for
heterogeneous telescope data. Must run AFTER PSF matching (stage 5 of 6).
Covers pixel-scale extraction priority, fallback WCS construction, the
no-overlap edge case, and the Lambda handler().
"""

from __future__ import annotations

import base64

import numpy as np
import pytest
from astropy.io import fits
from astropy.wcs import WCS
from saucepan_pipeline.reproject_frame import (
    extract_wcs,
    get_pixel_scale,
    get_pixel_scale_from_wcs,
    handler,
    reproject_frame,
    write_reproject_headers,
)


def _tan_wcs(ra=180.0, dec=0.0, pixel_scale_arcsec=1.0, shape=(64, 64)) -> WCS:
    h, w = shape
    hdr = fits.Header()
    hdr["NAXIS"] = 2
    hdr["NAXIS1"] = w
    hdr["NAXIS2"] = h
    hdr["CTYPE1"] = "RA---TAN"
    hdr["CTYPE2"] = "DEC--TAN"
    hdr["CRPIX1"] = w / 2.0
    hdr["CRPIX2"] = h / 2.0
    hdr["CRVAL1"] = ra
    hdr["CRVAL2"] = dec
    hdr["CDELT1"] = -pixel_scale_arcsec / 3600.0
    hdr["CDELT2"] = pixel_scale_arcsec / 3600.0
    return WCS(hdr)


# --- get_pixel_scale: priority order ----------------------------------------


def test_get_pixel_scale_prefers_sp_pixscale() -> None:
    hdr = fits.Header()
    hdr["SP_PIXSCALE"] = 1.5
    hdr["PIXSCALE"] = 9.9
    assert get_pixel_scale(hdr) == pytest.approx(1.5)


def test_get_pixel_scale_falls_back_to_pixscale() -> None:
    hdr = fits.Header()
    hdr["PIXSCALE"] = 2.2
    assert get_pixel_scale(hdr) == pytest.approx(2.2)


def test_get_pixel_scale_falls_back_to_cdelt2() -> None:
    hdr = fits.Header()
    hdr["CDELT2"] = 0.001
    assert get_pixel_scale(hdr) == pytest.approx(3.6)


def test_get_pixel_scale_falls_back_to_cd2_2() -> None:
    hdr = fits.Header()
    hdr["CD2_2"] = 0.0005
    assert get_pixel_scale(hdr) == pytest.approx(1.8)


def test_get_pixel_scale_zero_cdelt_is_ignored() -> None:
    hdr = fits.Header()
    hdr["CDELT2"] = 0.0
    hdr["CD2_2"] = 0.0
    assert get_pixel_scale(hdr) is None


def test_get_pixel_scale_none_when_nothing_present() -> None:
    assert get_pixel_scale(fits.Header()) is None


def test_get_pixel_scale_from_wcs() -> None:
    wcs = _tan_wcs(pixel_scale_arcsec=2.0)
    assert get_pixel_scale_from_wcs(wcs) == pytest.approx(2.0, rel=1e-3)


def test_get_pixel_scale_from_wcs_zero_cdelt_returns_zero() -> None:
    wcs = WCS(naxis=2)
    wcs.wcs.cdelt = [1.0, 0.0]
    assert get_pixel_scale_from_wcs(wcs) == 0.0


def test_get_pixel_scale_from_wcs_exception_returns_zero() -> None:
    class _BadWcsAttr:
        @property
        def cdelt(self):
            raise RuntimeError("boom")

    class _Fake:
        wcs = _BadWcsAttr()

    assert get_pixel_scale_from_wcs(_Fake()) == 0.0


# --- extract_wcs: real header vs fallback construction ----------------------


def test_extract_wcs_uses_real_header_wcs_when_present() -> None:
    hdr = fits.Header()
    hdr["CTYPE1"] = "RA---TAN"
    hdr["CTYPE2"] = "DEC--TAN"
    hdr["CRPIX1"] = 32.0
    hdr["CRPIX2"] = 32.0
    hdr["CRVAL1"] = 100.0
    hdr["CRVAL2"] = -20.0
    hdr["CDELT1"] = -0.0005
    hdr["CDELT2"] = 0.0005
    wcs = extract_wcs(hdr, (64, 64))
    assert wcs.has_celestial


def test_extract_wcs_falls_back_when_no_wcs_keywords() -> None:
    hdr = fits.Header()
    hdr["SP_RA"] = 45.0
    hdr["SP_DEC"] = 10.0
    hdr["SP_PIXSCALE"] = 1.0
    wcs = extract_wcs(hdr, (32, 32))
    assert wcs.has_celestial
    ra, dec = wcs.wcs.crval
    assert ra == pytest.approx(45.0)
    assert dec == pytest.approx(10.0)


def test_extract_wcs_fallback_uses_ra_dec_header_keys_when_sp_missing() -> None:
    hdr = fits.Header()
    hdr["RA"] = 12.0
    hdr["DEC"] = -5.0
    wcs = extract_wcs(hdr, (16, 16))
    assert wcs.wcs.crval[0] == pytest.approx(12.0)


def test_extract_wcs_fallback_defaults_ra_dec_to_zero() -> None:
    wcs = extract_wcs(fits.Header(), (16, 16))
    assert wcs.wcs.crval[0] == pytest.approx(0.0)
    assert wcs.wcs.crval[1] == pytest.approx(0.0)


# --- write_reproject_headers -------------------------------------------------


def test_write_reproject_headers_sets_expected_keys() -> None:
    hdr = fits.Header()
    write_reproject_headers(hdr, in_pixel_scale=1.0, out_pixel_scale=2.0, method="interp")
    assert hdr["SP_REPROJ"] is True
    assert hdr["SP_REPMETH"] == "interp"
    assert hdr["SP_IN_PX"] == pytest.approx(1.0)
    assert hdr["SP_OUT_PX"] == pytest.approx(2.0)
    assert hdr["SP_PX_RET"] == pytest.approx(0.5)


def test_write_reproject_headers_retention_capped_at_one() -> None:
    hdr = fits.Header()
    write_reproject_headers(hdr, in_pixel_scale=2.0, out_pixel_scale=1.0)
    assert hdr["SP_PX_RET"] == 1.0  # upsampling in pixel scale, capped at 1.0


def test_write_reproject_headers_zero_out_scale_defaults_retention_to_one() -> None:
    hdr = fits.Header()
    write_reproject_headers(hdr, in_pixel_scale=1.0, out_pixel_scale=0.0)
    assert hdr["SP_PX_RET"] == 1.0


# --- reproject_frame: normal + no-overlap edge cases -------------------------


def test_reproject_frame_same_wcs_preserves_data() -> None:
    data = np.arange(64 * 64, dtype=np.float32).reshape(64, 64)
    source_hdr = fits.Header()
    source_hdr["CTYPE1"] = "RA---TAN"
    source_hdr["CTYPE2"] = "DEC--TAN"
    source_hdr["CRPIX1"] = 32.0
    source_hdr["CRPIX2"] = 32.0
    source_hdr["CRVAL1"] = 180.0
    source_hdr["CRVAL2"] = 0.0
    source_hdr["CDELT1"] = -1.0 / 3600.0
    source_hdr["CDELT2"] = 1.0 / 3600.0
    target_wcs = _tan_wcs(ra=180.0, dec=0.0, pixel_scale_arcsec=1.0, shape=(64, 64))

    result, footprint = reproject_frame(data, source_hdr, target_wcs, (64, 64))
    assert result.shape == (64, 64)
    assert footprint.dtype == bool
    assert footprint.sum() > 0
    # Identity reprojection should recover the original data closely.
    assert np.nanmedian(np.abs(result[footprint] - data[footprint])) < 5.0


def test_reproject_frame_no_overlap_gives_empty_footprint() -> None:
    """Two fields on opposite sides of the sky: reprojecting one frame onto
    a completely disjoint target grid should yield zero coverage, not a
    crash — the WCS reprojection edge case named in the test brief."""
    data = np.full((32, 32), 500.0, dtype=np.float32)
    source_hdr = fits.Header()
    source_hdr["CTYPE1"] = "RA---TAN"
    source_hdr["CTYPE2"] = "DEC--TAN"
    source_hdr["CRPIX1"] = 16.0
    source_hdr["CRPIX2"] = 16.0
    source_hdr["CRVAL1"] = 10.0
    source_hdr["CRVAL2"] = 80.0  # near celestial pole, small field
    source_hdr["CDELT1"] = -0.0002
    source_hdr["CDELT2"] = 0.0002

    target_wcs = _tan_wcs(ra=200.0, dec=-70.0, pixel_scale_arcsec=1.0, shape=(32, 32))
    result, footprint = reproject_frame(data, source_hdr, target_wcs, (32, 32))
    assert result.shape == (32, 32)
    assert footprint.sum() == 0
    assert np.all(result[~footprint] == 0.0)  # nan_to_num'd background


def test_reproject_frame_writes_metadata_into_source_header() -> None:
    data = np.full((16, 16), 1.0, dtype=np.float32)
    source_hdr = fits.Header()
    source_hdr["SP_RA"] = 0.0
    source_hdr["SP_DEC"] = 0.0
    source_hdr["SP_PIXSCALE"] = 1.0
    target_wcs = _tan_wcs(ra=0.0, dec=0.0, pixel_scale_arcsec=1.0, shape=(16, 16))
    reproject_frame(data, source_hdr, target_wcs, (16, 16))
    assert "SP_REPROJ" in source_hdr
    assert source_hdr["SP_REPROJ"] is True


# --- Lambda handler ------------------------------------------------------------


def test_handler_unknown_action_returns_400() -> None:
    result = handler({"action": "nope"})
    assert result["statusCode"] == 400


def test_handler_round_trips_reprojection() -> None:
    data = np.full((16, 16), 42.0, dtype=np.float32)
    payload = {
        "action": "reproject",
        "data_base64": base64.b64encode(data.tobytes()).decode(),
        "shape": [16, 16],
        "header": {
            "SP_RA": 0.0,
            "SP_DEC": 0.0,
            "SP_PIXSCALE": 1.0,
        },
        "target_wcs_json": {
            "crpix": [8.0, 8.0],
            "crval": [0.0, 0.0],
            "cdelt": [-1.0 / 3600.0, 1.0 / 3600.0],
            "ctype": ["RA---TAN", "DEC--TAN"],
        },
        "target_shape": [16, 16],
    }
    result = handler(payload)
    assert result["statusCode"] == 200
    assert result["body"]["shape"] == [16, 16]
    assert result["body"]["footprint_shape"] == [16, 16]


def test_handler_missing_field_returns_500() -> None:
    result = handler({"action": "reproject"})
    assert result["statusCode"] == 500
    assert "error" in result["body"]
