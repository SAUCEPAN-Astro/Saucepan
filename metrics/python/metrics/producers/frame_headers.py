"""Producer: SP_* and related FITS headers."""

from __future__ import annotations

import typing

from metrics.observation import EntityContext

_HEADER_MAP: dict[str, str] = {
    "frame.ra_deg": "sp_ra",
    "frame.dec_deg": "sp_dec",
    "frame.tele_id": "sp_tele",
    "frame.target_name": "sp_tgtnam",
    "frame.instrument": "sp_instr",
    "frame.filter": "sp_filter",
    "frame.exptime_sec": "sp_exptime",
    "frame.dateobs": "sp_dateobs",
    "frame.mjd": "sp_mjd",
    "frame.hjd": "sp_hjd",
    "frame.bjd": "sp_bjd",
    "frame.norm_ver": "sp_norm",
    "frame.tier": "sp_tier",
    "frame.src": "sp_src",
    "frame.checksum": "sp_chksum",
    "frame.airmass": "sp_airmass",
    "frame.gain": "sp_gain",
    "frame.gain_iso": "sp_gainiso",
    "frame.rdnoise": "sp_rdnoise",
    "frame.site_lat": "sp_sitelat",
    "frame.site_lon": "sp_sitelon",
    "frame.site_elv": "sp_siteelv",
    "frame.calstat": "sp_calstat",
    "frame.bunit": "sp_bunit",
    "frame.pixscale": "sp_pixscale",
    "frame.snr": "sp_snr",
    "frame.bgmd": "sp_bgmd",
    "frame.bgnoi": "sp_bgnoi",
    "frame.stars": "sp_stars",
    "frame.spx": "sp_spx",
    "frame.fwhm_arcsec": "sp_fwhm",
    "frame.qual": "sp_qual",
    "frame.nx": "sp_nx",
    "frame.ny": "sp_ny",
    "frame.pver": "sp_pver",
    "frame.bin_x": "sp_binx",
    "frame.bin_y": "sp_biny",
    "frame.astrmsr_arcsec": "sp_astrmsr",
}

_WCS_PLATE_KEYS = ("ctype1", "crval1", "crpix1")


def _read_headers(path: str) -> dict[str, typing.Any]:
    try:
        from grading.fits_reader import read_sp_headers
    except ImportError:
        from astropy.io import fits

        with fits.open(path, memmap=True) as hdul:
            hdr = hdul[0].header
            return {k.lower(): hdr.get(k) for k in hdr.keys()}
    base = read_sp_headers(path)
    try:
        from astropy.io import fits

        with fits.open(path, memmap=True) as hdul:
            hdr = hdul[0].header
            for extra in (
                "SP_NORM",
                "SP_TIER",
                "SP_SRC",
                "SP_TGTNAM",
                "SP_INSTR",
                "SP_NX",
                "SP_NY",
                "SP_PVER",
                "SP_MJD",
                "SP_HJD",
                "SP_BJD",
                "SP_PROV",
                "SP_BINX",
                "SP_BINY",
                "SP_GAINISO",
                "SP_BKGRMS",
                "SP_BKGMED",
                "SP_ASTRMSR",
                "SP_BUNIT",
            ):
                key = extra.lower()
                if key not in base and extra in hdr:
                    base[key] = hdr[extra]
    except ImportError:
        pass
    return base


def _bunit_is_electron(bunit: typing.Any) -> bool:
    if bunit is None:
        return False
    return "electron" in str(bunit).lower()


def _plate_solve_ok(hdr: dict[str, typing.Any]) -> int:
    return int(all(hdr.get(k) is not None for k in _WCS_PLATE_KEYS))


def _derived_background(hdr: dict[str, typing.Any]) -> dict[str, typing.Any]:
    """Alias bkgmed/bkgrms from SP_* headers when units are electron."""
    out: dict[str, typing.Any] = {}
    if not _bunit_is_electron(hdr.get("sp_bunit")):
        return out
    bkgmed = hdr.get("sp_bkgmed")
    if bkgmed is None:
        bkgmed = hdr.get("sp_bgmd")
    if bkgmed is not None:
        out["frame.bkgmed"] = bkgmed
    bkgrms = hdr.get("sp_bkgrms")
    if bkgrms is not None:
        out["frame.bkgrms"] = bkgrms
    return out


def produce(ctx: EntityContext) -> dict[str, typing.Any]:
    path = ctx.get("staged_path")
    if not path:
        return {}
    hdr = _read_headers(path)
    out: dict[str, typing.Any] = {}
    for metric_id, hdr_key in _HEADER_MAP.items():
        val = hdr.get(hdr_key)
        if hdr_key == "sp_tele" and val is None:
            val = ctx.get("telescope_id")
        if val is not None:
            out[metric_id] = val

    out["frame.plate_solve_ok"] = _plate_solve_ok(hdr)
    astrmsr = hdr.get("sp_astrmsr")
    if astrmsr is not None:
        out["frame.wcs_distortion"] = astrmsr

    out.update(_derived_background(hdr))
    if hdr.get("sp_prov") is not None:
        out["frame.prov_uri"] = "fits://header#SP_PROV"
    return out
