"""Tests for HJD/BJD derivation."""

from __future__ import annotations

import pathlib
import sys

# metrics/ moved out from under SaucepanServer/compute-server/ (#426 metrics
# consolidation); normalize/ stayed put, so reach it explicitly instead of
# assuming a co-located sibling.
REPO_ROOT = pathlib.Path(__file__).resolve().parents[4]
sys.path.insert(0, str(REPO_ROOT / "SaucepanServer" / "compute-server"))

from normalize.time_headers import (  # noqa: E402
    compute_bjd,
    compute_hjd,
    derive_time_headers,
)


def test_compute_hjd_bjd_known_site():
    dateobs = "2024-01-15T22:30:00"
    ra, dec = 83.633, 22.0
    lat, lon, elv = 32.7, -117.2, 100.0

    hjd = compute_hjd(dateobs, ra, dec, site_lat=lat, site_lon=lon, site_elv=elv)
    bjd, prov = compute_bjd(
        dateobs,
        ra,
        dec,
        site_lat=lat,
        site_lon=lon,
        site_elv=elv,
        timesys="UTC",
    )

    assert hjd is not None
    assert bjd is not None
    assert 2460000 < hjd < 2470000
    assert 2460000 < bjd < 2470000
    assert abs(hjd - bjd) < 0.01
    assert prov == "ASSUMED_UTC"


def test_bjd_gps_provenance():
    _, prov = compute_bjd(
        "2024-01-15T22:30:00",
        83.633,
        22.0,
        site_lat=32.7,
        site_lon=-117.2,
        timesys="GPS",
    )
    assert prov == "GPS"


def test_derive_time_headers_from_resolved():
    resolved = {
        "SP_DATEOBS": "2024-01-15T22:30:00",
        "SP_RA": 83.633,
        "SP_DEC": 22.0,
        "SP_SITELAT": 32.7,
        "SP_SITELON": -117.2,
        "SP_TIMESYS": "NTP",
    }
    out = derive_time_headers(resolved)
    assert "SP_HJD" in out
    assert "SP_BJD" in out
    assert out["SP_BJD_PROV"] == "ASSUMED_UTC"


def test_derive_time_headers_missing_coords():
    assert derive_time_headers({"SP_DATEOBS": "2024-01-15T22:30:00"}) == {}
