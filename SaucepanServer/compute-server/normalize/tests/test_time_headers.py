"""normalize/time_headers.py — HJD/BJD derivation from SP_ headers."""

from __future__ import annotations

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))

from normalize.time_headers import compute_bjd, compute_hjd, derive_time_headers

_SITE = dict(site_lat=31.9583, site_lon=-111.6, site_elv=2096.0)


def test_compute_hjd_returns_none_without_site() -> None:
    assert compute_hjd("2024-01-01T00:00:00", 180.0, 0.0) is None


def test_compute_hjd_returns_value_with_full_inputs() -> None:
    hjd = compute_hjd("2024-01-01T00:00:00", 180.0, 0.0, **_SITE)
    assert hjd is not None
    assert hjd > 2400000.0  # sane JD range


def test_compute_hjd_returns_none_on_bad_dateobs() -> None:
    assert compute_hjd("not-a-date", 180.0, 0.0, **_SITE) is None


def test_compute_bjd_prov_defaults_to_assumed_utc() -> None:
    bjd, prov = compute_bjd("2024-01-01T00:00:00", 180.0, 0.0, **_SITE)
    assert prov == "ASSUMED_UTC"
    assert bjd is not None


def test_compute_bjd_prov_gps_when_timesys_gps() -> None:
    bjd, prov = compute_bjd("2024-01-01T00:00:00", 180.0, 0.0, timesys="GPS", **_SITE)
    assert prov == "GPS"
    assert bjd is not None


def test_compute_bjd_none_without_site_still_reports_prov() -> None:
    bjd, prov = compute_bjd("2024-01-01T00:00:00", 180.0, 0.0, timesys="GPS")
    assert bjd is None
    assert prov == "GPS"


def test_compute_bjd_returns_none_on_bad_dateobs() -> None:
    bjd, prov = compute_bjd("garbage", 180.0, 0.0, **_SITE)
    assert bjd is None
    assert prov == "ASSUMED_UTC"


def test_derive_time_headers_empty_without_dateobs() -> None:
    out = derive_time_headers({"SP_RA": 180.0, "SP_DEC": 0.0})
    assert out == {}


def test_derive_time_headers_empty_without_ra_dec() -> None:
    out = derive_time_headers({"SP_DATEOBS": "2024-01-01T00:00:00"})
    assert out == {}


def test_derive_time_headers_full_from_resolved_site() -> None:
    resolved = {
        "SP_DATEOBS": "2024-01-01T00:00:00",
        "SP_RA": 180.0,
        "SP_DEC": 0.0,
        "SP_SITELAT": _SITE["site_lat"],
        "SP_SITELON": _SITE["site_lon"],
        "SP_SITEELV": _SITE["site_elv"],
    }
    out = derive_time_headers(resolved)
    assert "SP_HJD" in out
    assert "SP_BJD" in out
    assert out["SP_BJD_PROV"] == "ASSUMED_UTC"


def test_derive_time_headers_falls_back_to_source_header_site_keys() -> None:
    resolved = {
        "SP_DATEOBS": "2024-01-01T00:00:00",
        "SP_RA": 180.0,
        "SP_DEC": 0.0,
    }
    source_headers = {
        "SITELAT": _SITE["site_lat"],
        "SITELON": _SITE["site_lon"],
    }
    out = derive_time_headers(resolved, source_headers)
    assert "SP_HJD" in out


def test_derive_time_headers_source_header_alt_lat_lon_keys() -> None:
    resolved = {"SP_DATEOBS": "2024-01-01T00:00:00", "SP_RA": 180.0, "SP_DEC": 0.0}
    source_headers = {"OBSLAT": _SITE["site_lat"], "OBSLONG": _SITE["site_lon"]}
    out = derive_time_headers(resolved, source_headers)
    assert "SP_HJD" in out


def test_derive_time_headers_no_site_anywhere_yields_no_hjd_bjd() -> None:
    resolved = {"SP_DATEOBS": "2024-01-01T00:00:00", "SP_RA": 180.0, "SP_DEC": 0.0}
    out = derive_time_headers(resolved, {})
    assert out == {}


def test_derive_time_headers_skips_when_already_resolved() -> None:
    resolved = {
        "SP_DATEOBS": "2024-01-01T00:00:00",
        "SP_RA": 180.0,
        "SP_DEC": 0.0,
        "SP_SITELAT": _SITE["site_lat"],
        "SP_SITELON": _SITE["site_lon"],
        "SP_HJD": 12345.0,
        "SP_BJD": 12345.0,
    }
    out = derive_time_headers(resolved)
    # Already-resolved keys are not recomputed/overwritten in the output.
    assert "SP_HJD" not in out
    assert "SP_BJD" not in out


def test_derive_time_headers_timesys_from_source_headers() -> None:
    resolved = {
        "SP_DATEOBS": "2024-01-01T00:00:00",
        "SP_RA": 180.0,
        "SP_DEC": 0.0,
        "SP_SITELAT": _SITE["site_lat"],
        "SP_SITELON": _SITE["site_lon"],
    }
    out = derive_time_headers(resolved, {"TIMESYS": "GPS"})
    assert out["SP_BJD_PROV"] == "GPS"
