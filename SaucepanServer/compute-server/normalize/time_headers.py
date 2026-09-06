"""
Heliocentric and barycentric Julian dates from observation metadata.

Uses astropy light-travel-time corrections when DATE-OBS, RA/Dec, and site
coordinates are available.
"""

from __future__ import annotations

import logging
import typing

log = logging.getLogger(__name__)

_DEFAULT_ELEV_M = 0.0


def _site_location(
    site_lat: float | None,
    site_lon: float | None,
    site_elv: float | None,
):
    import astropy.units as u
    from astropy.coordinates import EarthLocation

    if site_lat is None or site_lon is None:
        return None
    height = float(site_elv) if site_elv is not None else _DEFAULT_ELEV_M
    return EarthLocation(
        lat=float(site_lat) * u.deg,
        lon=float(site_lon) * u.deg,
        height=height * u.m,
    )


def compute_hjd(
    dateobs: str,
    ra_deg: float,
    dec_deg: float,
    *,
    site_lat: float | None = None,
    site_lon: float | None = None,
    site_elv: float | None = None,
) -> float | None:
    """
    Heliocentric Julian Date (JD, not MJD).

    Returns None when astropy is unavailable or required inputs are missing.
    """
    try:
        import astropy.units as u
        from astropy.coordinates import SkyCoord
        from astropy.time import Time
    except ImportError:
        return None

    location = _site_location(site_lat, site_lon, site_elv)
    if location is None:
        return None

    try:
        obstime = Time(dateobs, format="isot", scale="utc")
        coord = SkyCoord(ra=float(ra_deg) * u.deg, dec=float(dec_deg) * u.deg, frame="icrs")
        lt = obstime.light_travel_time(coord, "heliocentric", location=location)
        return float((obstime.utc + lt).jd)
    except Exception as exc:
        log.debug("HJD computation failed: %s", exc)
        return None


def compute_bjd(
    dateobs: str,
    ra_deg: float,
    dec_deg: float,
    *,
    site_lat: float | None = None,
    site_lon: float | None = None,
    site_elv: float | None = None,
    timesys: str | None = None,
) -> tuple[float | None, str]:
    """
    Barycentric Julian Date (JD) and provenance tag.

    When TIMESYS is not GPS, BJD is still computed from the UTC timestamp and
    ``SP_BJD_PROV`` is set to ``ASSUMED_UTC``.
    """
    prov = "GPS" if (timesys or "").strip().upper() == "GPS" else "ASSUMED_UTC"

    try:
        import astropy.units as u
        from astropy.coordinates import SkyCoord
        from astropy.time import Time
    except ImportError:
        return None, prov

    location = _site_location(site_lat, site_lon, site_elv)
    if location is None:
        return None, prov

    try:
        obstime = Time(dateobs, format="isot", scale="utc")
        coord = SkyCoord(ra=float(ra_deg) * u.deg, dec=float(dec_deg) * u.deg, frame="icrs")
        lt = obstime.light_travel_time(coord, "barycentric", location=location)
        return float((obstime.utc + lt).jd), prov
    except Exception as exc:
        log.debug("BJD computation failed: %s", exc)
        return None, prov


def derive_time_headers(
    resolved: typing.Mapping[str, typing.Any],
    source_headers: typing.Mapping[str, typing.Any] | None = None,
) -> dict[str, typing.Any]:
    """
    Derive SP_HJD / SP_BJD / SP_BJD_PROV when DATE-OBS and coordinates exist.

    Called from normalize after SP_DATEOBS is resolved.
    """
    out: dict[str, typing.Any] = {}
    dateobs = resolved.get("SP_DATEOBS")
    ra = resolved.get("SP_RA")
    dec = resolved.get("SP_DEC")
    if not dateobs or ra is None or dec is None:
        return out

    site_lat = resolved.get("SP_SITELAT")
    site_lon = resolved.get("SP_SITELON")
    site_elv = resolved.get("SP_SITEELV")
    if site_lat is None and source_headers:
        for key in ("SITELAT", "OBSLAT", "LAT_OBS"):
            if key in source_headers:
                try:
                    site_lat = float(source_headers[key])
                    break
                except (TypeError, ValueError):
                    pass
    if site_lon is None and source_headers:
        for key in ("SITELON", "SITELONG", "OBSLONG", "LON_OBS"):
            if key in source_headers:
                try:
                    site_lon = float(source_headers[key])
                    break
                except (TypeError, ValueError):
                    pass

    timesys = resolved.get("SP_TIMESYS")
    if timesys is None and source_headers:
        timesys = source_headers.get("TIMESYS") or source_headers.get("TIME-SYS")

    kwargs = {
        "site_lat": site_lat,
        "site_lon": site_lon,
        "site_elv": site_elv,
    }

    if "SP_HJD" not in resolved:
        hjd = compute_hjd(dateobs, float(ra), float(dec), **kwargs)
        if hjd is not None:
            out["SP_HJD"] = round(hjd, 8)

    if "SP_BJD" not in resolved:
        bjd, prov = compute_bjd(
            dateobs,
            float(ra),
            float(dec),
            timesys=str(timesys) if timesys is not None else None,
            **kwargs,
        )
        if bjd is not None:
            out["SP_BJD"] = round(bjd, 8)
            out["SP_BJD_PROV"] = prov

    return out
