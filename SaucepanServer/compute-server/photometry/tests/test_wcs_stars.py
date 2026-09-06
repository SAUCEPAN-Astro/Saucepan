"""RA/Dec -> pixel projection for campaign comparison stars (#203)."""

from __future__ import annotations

import numpy as np
from astropy.wcs import WCS
from photometry import wcs_stars


def _tan_wcs(nx=200, ny=200, ra0=90.0, dec0=10.0, scale_deg=1.5 / 3600.0):
    w = WCS(naxis=2)
    w.wcs.crpix = [nx / 2, ny / 2]
    w.wcs.cdelt = [-scale_deg, scale_deg]
    w.wcs.crval = [ra0, dec0]
    w.wcs.ctype = ["RA---TAN", "DEC--TAN"]
    return w


def test_radec_comps_land_on_known_pixels():
    w = _tan_wcs()
    want = [(50.0, 60.0), (120.0, 175.0), (10.0, 10.0)]
    ra, dec = w.all_pix2world([p[0] for p in want], [p[1] for p in want], 0)
    stars = [
        {"id": f"c{i}", "ra": float(r), "dec": float(d), "role": "comp"}
        for i, (r, d) in enumerate(zip(ra, dec))
    ]
    out = wcs_stars.project_comp_stars(stars, w, (200, 200))
    assert len(out) == 3
    for got, (wx, wy) in zip(out, want):
        assert got["source"] == "wcs"
        assert got["in_frame"] is True
        np.testing.assert_allclose(got["x"], wx, atol=1e-6)
        np.testing.assert_allclose(got["y"], wy, atol=1e-6)


def test_star_off_sensor_flagged_not_in_frame():
    w = _tan_wcs()
    ra, dec = w.all_pix2world(500.0, 500.0, 0)
    out = wcs_stars.project_comp_stars(
        [{"id": "off", "ra": float(ra), "dec": float(dec)}], w, (200, 200)
    )
    assert len(out) == 1
    assert out[0]["in_frame"] is False


def test_pixel_native_star_passes_through_without_wcs():
    out = wcs_stars.project_comp_stars(
        [{"id": "legacy", "x": 12.0, "y": 34.0}], None, (100, 100)
    )
    assert out[0]["source"] == "pixel"
    assert out[0]["x"] == 12.0 and out[0]["y"] == 34.0
    assert out[0]["in_frame"] is True


def test_pixel_native_star_kept_even_when_wcs_present():
    w = _tan_wcs()
    out = wcs_stars.project_comp_stars(
        [{"id": "legacy", "x_pix": 5.0, "y_pix": 7.0}], w, (100, 100)
    )
    assert out[0]["source"] == "pixel"
    assert (out[0]["x"], out[0]["y"]) == (5.0, 7.0)


def test_star_with_neither_position_is_dropped():
    out = wcs_stars.project_comp_stars([{"id": "nowhere"}], _tan_wcs(), (100, 100))
    assert out == []


def test_shape_none_skips_bounds_check():
    w = _tan_wcs()
    ra, dec = w.all_pix2world(9999.0, 9999.0, 0)
    out = wcs_stars.project_comp_stars(
        [{"id": "x", "ra": float(ra), "dec": float(dec)}], w, None
    )
    assert out[0]["in_frame"] is True


def test_wcs_from_header_roundtrip():
    w = _tan_wcs()
    hdr = dict(w.to_header())
    built = wcs_stars.wcs_from_header(hdr)
    assert built is not None
    np.testing.assert_allclose(built.wcs.crval, [90.0, 10.0])


def test_wcs_from_header_none_when_no_celestial():
    assert wcs_stars.wcs_from_header({"NAXIS": 2, "FOO": 1}) is None
