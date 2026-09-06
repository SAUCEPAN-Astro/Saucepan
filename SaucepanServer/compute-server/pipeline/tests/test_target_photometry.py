"""target_photometry.py — position-anchored aperture photometry at a known
sky position (#471). Covers off-frame apertures, WCS projection failures,
non-finite pixel positions, and the annulus-fully-off-frame edge case.
"""

from __future__ import annotations

import numpy as np
import pytest
from astropy.io import fits
from astropy.wcs import WCS
from saucepan_pipeline.target_photometry import measure_target_flux


def _tan_wcs(ra=180.0, dec=0.0, pixel_scale_arcsec=1.0, shape=(64, 64)) -> WCS:
    h, w = shape
    hdr = fits.Header()
    hdr["CTYPE1"] = "RA---TAN"
    hdr["CTYPE2"] = "DEC--TAN"
    hdr["CRPIX1"] = w / 2.0
    hdr["CRPIX2"] = h / 2.0
    hdr["CRVAL1"] = ra
    hdr["CRVAL2"] = dec
    hdr["CDELT1"] = -pixel_scale_arcsec / 3600.0
    hdr["CDELT2"] = pixel_scale_arcsec / 3600.0
    return WCS(hdr)


def test_measure_target_flux_recovers_known_flux_at_center() -> None:
    wcs = _tan_wcs(shape=(64, 64))
    data = np.zeros((64, 64), dtype=np.float32)
    # Inject a flat-topped source at the WCS center with known total flux.
    yy, xx = np.mgrid[:64, :64]
    r = np.sqrt((xx - 32) ** 2 + (yy - 32) ** 2)
    data[r <= 10] = 100.0
    result = measure_target_flux(data, wcs, ra_deg=180.0, dec_deg=0.0, aperture_radius_px=10.0)
    assert result["ok"] is True
    assert result["flux"] > 0
    # WCS pixel convention: CRPIX=32.0 (1-indexed) -> pixel 31.0 at origin=0.
    assert result["x"] == pytest.approx(31.0, abs=1.5)
    assert result["y"] == pytest.approx(31.0, abs=1.5)


def test_measure_target_flux_off_frame_reports_not_ok() -> None:
    wcs = _tan_wcs(shape=(32, 32))
    data = np.zeros((32, 32), dtype=np.float32)
    # Target far outside the field of view.
    result = measure_target_flux(data, wcs, ra_deg=180.0, dec_deg=45.0)
    assert result["ok"] is False
    assert result["reason"] == "aperture off frame"


def test_measure_target_flux_wcs_projection_failure_reports_not_ok() -> None:
    class _BrokenWcs:
        def all_world2pix(self, ra, dec, origin):
            raise ValueError("bad wcs")

    data = np.zeros((16, 16), dtype=np.float32)
    result = measure_target_flux(data, _BrokenWcs(), ra_deg=1.0, dec_deg=1.0)
    assert result["ok"] is False
    assert "wcs projection failed" in result["reason"]
    assert result["x"] is None
    assert result["y"] is None


def test_measure_target_flux_non_finite_pixel_position() -> None:
    class _NanWcs:
        def all_world2pix(self, ra, dec, origin):
            return np.nan, np.nan

    data = np.zeros((16, 16), dtype=np.float32)
    result = measure_target_flux(data, _NanWcs(), ra_deg=1.0, dec_deg=1.0)
    assert result["ok"] is False
    assert result["reason"] == "non-finite pixel position"


def test_measure_target_flux_annulus_fully_off_frame_bg_defaults_zero() -> None:
    """Aperture on-frame near a corner, but the background annulus falls
    entirely off the small array -> bg_per_px must default to 0.0 rather
    than crash on an empty nanmedian."""
    data = np.full((10, 10), 5.0, dtype=np.float32)
    wcs = _tan_wcs(shape=(10, 10))
    # Place target near pixel (0,0) with a tiny aperture so annulus (18-26px)
    # falls entirely outside the 10x10 array.
    result = measure_target_flux(
        data,
        wcs,
        ra_deg=180.0,
        dec_deg=0.0,
        aperture_radius_px=1.0,
        annulus_inner_px=18.0,
        annulus_outer_px=26.0,
    )
    # Center of a 10x10 TAN WCS is at pixel (5,5); aperture there is on-frame.
    assert result["ok"] is True
    assert result["bg_per_px"] == 0.0
    assert result["flux"] == pytest.approx(result["raw_aperture_sum"])


def test_measure_target_flux_subtracts_annulus_background() -> None:
    wcs = _tan_wcs(shape=(64, 64))
    data = np.full((64, 64), 20.0, dtype=np.float32)  # uniform background
    yy, xx = np.mgrid[:64, :64]
    # Injected disk (r<=8) sits safely inside the aperture (r<=15) even with
    # the ~1px CRPIX/all_world2pix origin offset, so the full injected flux
    # is captured regardless of exact sub-pixel alignment.
    r = np.sqrt((xx - 32) ** 2 + (yy - 32) ** 2)
    n_injected = int((r <= 8).sum())
    data[r <= 8] += 100.0
    result = measure_target_flux(data, wcs, ra_deg=180.0, dec_deg=0.0, aperture_radius_px=15.0)
    assert result["ok"] is True
    assert result["bg_per_px"] == pytest.approx(20.0, abs=1.0)
    # Flux should approximately equal the injected excess over background.
    assert result["flux"] == pytest.approx(100.0 * n_injected, rel=0.05)


def test_measure_target_flux_nan_pixels_ignored_via_nansum() -> None:
    wcs = _tan_wcs(shape=(32, 32))
    data = np.full((32, 32), 10.0, dtype=np.float32)
    data[16, 16] = np.nan
    result = measure_target_flux(data, wcs, ra_deg=180.0, dec_deg=0.0, aperture_radius_px=5.0)
    assert result["ok"] is True
    assert np.isfinite(result["flux"])


def _full_frame_reference(data, wcs, ra_deg, dec_deg, **kwargs):
    """Reference implementation matching the pre-bounded computation."""
    x, y = wcs.all_world2pix(ra_deg, dec_deg, 0)
    x, y = float(x), float(y)
    h, w = data.shape
    yy, xx = np.mgrid[0:h, 0:w]
    r = np.sqrt((xx - x) ** 2 + (yy - y) ** 2)
    aperture_mask = r <= kwargs.get("aperture_radius_px", 12.0)
    annulus_mask = (r >= kwargs.get("annulus_inner_px", 18.0)) & (
        r <= kwargs.get("annulus_outer_px", 26.0)
    )
    if not np.any(aperture_mask):
        return {"ok": False, "reason": "aperture off frame", "x": x, "y": y}
    aperture_sum = float(np.nansum(data[aperture_mask]))
    n_ap_px = int(np.sum(aperture_mask))
    bg_per_px = float(np.nanmedian(data[annulus_mask])) if np.any(annulus_mask) else 0.0
    return {
        "ok": True,
        "x": x,
        "y": y,
        "raw_aperture_sum": aperture_sum,
        "bg_per_px": bg_per_px,
        "n_aperture_px": n_ap_px,
        "flux": aperture_sum - bg_per_px * n_ap_px,
    }


@pytest.mark.parametrize("position", [(32.0, 32.0), (0.2, 0.3)])
def test_measure_target_flux_bounded_grid_matches_full_frame(position) -> None:
    shape = (64, 64)
    wcs = _tan_wcs(shape=shape)
    x, y = position
    # The WCS is constructed so this sky position maps to the requested pixel.
    sky_ra, sky_dec = wcs.all_pix2world(x, y, 0)
    rng = np.random.default_rng(75)
    data = rng.normal(size=shape).astype(np.float32)
    data[0, 0] = np.nan
    kwargs = {"aperture_radius_px": 7.5, "annulus_inner_px": 11.0, "annulus_outer_px": 16.0}

    bounded = measure_target_flux(data, wcs, sky_ra, sky_dec, **kwargs)
    reference = _full_frame_reference(data, wcs, sky_ra, sky_dec, **kwargs)
    assert bounded == reference
