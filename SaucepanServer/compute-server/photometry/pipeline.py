"""Photometry pipeline: detect → plate solve (cached) → ZP → depth."""

from __future__ import annotations

import logging
import math
import os
import typing

import numpy as np
from grading.fits_limits import ensure_fits_loadable

from photometry import platesolve_cache, scintillation, zeropoint

logger = logging.getLogger(__name__)

_WCS_KEYS = ("CTYPE1", "CRVAL1", "CRPIX1")
_ZP_RMS_FLAG_THRESHOLD = 0.05

try:
    from astropy.io import fits
except ImportError:  # pragma: no cover
    fits = None  # type: ignore[assignment]


def run_photometry(
    fits_path: str,
    ctx: dict[str, typing.Any],
    *,
    update_fits: bool = True,
) -> dict[str, typing.Any]:
    """
    Run photometry for one frame. Fail-open: always returns a summary dict.

    Steps: source detection → plate solve (shared cache) → ZP fit → depth/flag.
    """
    summary: dict[str, typing.Any] = {
        "fits_path": fits_path,
        "upload_id": ctx.get("upload_id"),
        "status": "ok",
        "plate_solve_cached": False,
    }
    try:
        data, hdr = _load_image(fits_path)
        checksum = _frame_checksum(hdr, ctx)
        cache_key = platesolve_cache.make_key(ctx.get("upload_id"), checksum)

        sources = _detect_sources(data, hdr)
        summary["n_sources"] = len(sources.get("flux", []))

        plate = _plate_solve(fits_path, hdr, sources, cache_key)
        summary["plate_solve_cached"] = plate.get("cached", False)
        summary["plate_solve_ok"] = plate.get("ok", False)

        zp = _fit_zeropoint(sources, plate, hdr, ctx=ctx)
        depth = _measure_depth(data, hdr, zp)
        phot_flag = _phot_flag(zp, plate)
        sigma_scint = _scintillation_prior(hdr, ctx)

        summary.update(
            {
                "skymag": depth["skymag"],
                "zp": zp["zp"],
                "zp_rms": zp["zp_rms"],
                "n_cal_stars": zp["n_cal_stars"],
                "zp_ok": zp.get("ok", False),
                "zp_reason": zp.get("reason"),
                "zp_catalog": zp.get("catalog"),
                "zp_epoch": zp.get("epoch"),
                "limmag_5sigma": depth["limmag_5sigma"],
                "sigma_scint": sigma_scint,
                "phot_flag": phot_flag,
                "checksum": checksum,
            }
        )

        if update_fits and fits is not None:
            _write_headers(fits_path, summary)

    except Exception as exc:
        logger.exception("photometry pipeline failed path=%s", fits_path)
        summary["status"] = "error"
        summary["error"] = str(exc)
        summary.setdefault("phot_flag", 4)

    return summary


def _load_image(path: str) -> tuple[np.ndarray, dict[str, typing.Any]]:
    if fits is None:
        raise ImportError("astropy required for photometry")
    with fits.open(path, memmap=True) as hdul:
        ensure_fits_loadable(path, hdul[0].header)
        data = np.asarray(hdul[0].data, dtype=np.float32)
        hdr = {k: hdul[0].header[k] for k in hdul[0].header.keys()}
    return data, hdr


def _frame_checksum(hdr: dict[str, typing.Any], ctx: dict[str, typing.Any]) -> str | None:
    for key in ("SP_CHKSUM", "CHECKSUM"):
        val = hdr.get(key)
        if val is not None and str(val).strip():
            return str(val).strip()
    catalog = ctx.get("_catalog") or {}
    if catalog.get("checksum_sha256"):
        return str(catalog["checksum_sha256"])
    return ctx.get("checksum_sha256")


def _detect_sources(data: np.ndarray, hdr: dict[str, typing.Any]) -> dict[str, typing.Any]:
    """Detect point sources; photutils when available, else lightweight stub."""
    try:
        from photutils.detection import DAOStarFinder

        bg = float(np.median(data))
        noise = float(np.std(data[data < bg + 3 * np.std(data)])) or 1.0
        fwhm_px = _fwhm_pixels(hdr)
        finder = DAOStarFinder(fwhm=fwhm_px, threshold=5.0 * noise)
        table = finder(data - bg)
        if table is None or len(table) == 0:
            return _stub_sources(data, hdr)
        return {
            "x": np.asarray(table["xcentroid"]),
            "y": np.asarray(table["ycentroid"]),
            "flux": np.asarray(table["flux"]),
            "method": "photutils",
        }
    except ImportError:
        return _stub_sources(data, hdr)
    except Exception:
        logger.exception("photutils detection failed; using stub")
        return _stub_sources(data, hdr)


def _stub_sources(data: np.ndarray, hdr: dict[str, typing.Any]) -> dict[str, typing.Any]:
    """Crude source list from bright pixels (fail-open stub)."""
    bg = float(np.median(data))
    noise = float(np.std(data)) or 1.0
    mask = data > (bg + 5.0 * noise)
    ys, xs = np.where(mask)
    if len(xs) == 0:
        h, w = data.shape
        xs = np.array([w / 2.0])
        ys = np.array([h / 2.0])
        flux = np.array([float(data[int(ys[0]), int(xs[0])] - bg)])
    else:
        step = max(1, len(xs) // 50)
        xs = xs[::step].astype(float)
        ys = ys[::step].astype(float)
        flux = data[ys.astype(int), xs.astype(int)] - bg
    return {"x": xs, "y": ys, "flux": np.maximum(flux, 1.0), "method": "stub"}


def _fwhm_pixels(hdr: dict[str, typing.Any]) -> float:
    for key in ("SP_FWHM", "SEEING"):
        if key in hdr:
            try:
                arcsec = float(hdr[key])
                # Explicit `is None` chain, not `a or b or 1.0`: an explicitly
                # supplied SP_PIXSCALE=0.0 (or negative) is invalid input, not a
                # missing value, and must not be silently replaced by the 1.0
                # default. A present-but-invalid pixscale fails the `> 0` guard
                # and falls through to the default FWHM below.
                raw_pixscale = hdr.get("SP_PIXSCALE")
                if raw_pixscale is None:
                    raw_pixscale = hdr.get("PIXSCALE")
                pixscale = 1.0 if raw_pixscale is None else float(raw_pixscale)
                if pixscale > 0:
                    return max(2.0, arcsec / pixscale)
            except (TypeError, ValueError):
                pass
    return 3.0


def _wcs_present(hdr: dict[str, typing.Any]) -> bool:
    return all(hdr.get(k) is not None for k in _WCS_KEYS)


def _plate_solve(
    fits_path: str,
    hdr: dict[str, typing.Any],
    sources: dict[str, typing.Any],
    cache_key: platesolve_cache.CacheKey | None,
) -> dict[str, typing.Any]:
    cached = platesolve_cache.get(cache_key)
    if cached is not None:
        return {**cached, "cached": True}

    if _wcs_present(hdr):
        result = {
            "ok": True,
            "method": "existing_wcs",
            "ra": float(hdr.get("CRVAL1", 0.0)),
            "dec": float(hdr.get("CRVAL2", 0.0)),
            "astrmsr_arcsec": float(hdr.get("SP_ASTRMSR") or 0.5),
        }
    else:
        result = _stub_plate_solve(hdr, sources)

    platesolve_cache.put(cache_key, result)
    return {**result, "cached": False}


def _stub_plate_solve(
    hdr: dict[str, typing.Any], sources: dict[str, typing.Any]
) -> dict[str, typing.Any]:
    """Placeholder astrometric solution until server-side ASTAP is wired."""
    ra = hdr.get("SP_RA") or hdr.get("RA")
    dec = hdr.get("SP_DEC") or hdr.get("DEC")
    n = len(sources.get("flux", []))
    ok = ra is not None and dec is not None and n >= 5
    return {
        "ok": ok,
        "method": "stub",
        "ra": float(ra) if ra is not None else None,
        "dec": float(dec) if dec is not None else None,
        "astrmsr_arcsec": 1.0 if ok else None,
        "n_sources_used": n,
    }


def _fit_zeropoint(
    sources: dict[str, typing.Any],
    plate: dict[str, typing.Any],
    hdr: dict[str, typing.Any],
    *,
    ctx: dict[str, typing.Any] | None = None,
) -> dict[str, typing.Any]:
    """Photometric zeropoint.

    Real path: catalog-matched fit in ``photometry.zeropoint`` — fails closed
    (``ok=False``, no number) without an external reference or with too few
    calibrators. The old self-referential stub survives *only* behind
    ``PHOT_STUB=1`` for offline demos and is logged loudly every call.
    """
    if os.environ.get("PHOT_STUB") == "1":
        logger.warning(
            "PHOT_STUB=1: photometry zeropoint is the SELF-REFERENTIAL STUB "
            "(no catalog match, not science-grade) — path=%s",
            (ctx or {}).get("upload_id"),
        )
        return _fit_zeropoint_stub(sources, plate, hdr)

    return zeropoint.zeropoint_for_frame(sources, plate, hdr, ctx or {})


def _fit_zeropoint_stub(
    sources: dict[str, typing.Any],
    plate: dict[str, typing.Any],
    hdr: dict[str, typing.Any],
) -> dict[str, typing.Any]:
    """DEPRECATED demo-only ZP. Derives its 'reference' from the same
    instrumental magnitudes it reports — circular, not a calibration. Reachable
    only via ``PHOT_STUB=1``."""
    flux = np.asarray(sources.get("flux", []), dtype=float)
    if len(flux) == 0 or not plate.get("ok"):
        return {"zp": None, "zp_rms": None, "n_cal_stars": 0, "ok": False, "reason": "stub_no_input"}

    exptime = float(hdr.get("SP_EXPTIME") or hdr.get("EXPTIME") or 1.0)
    inst_mags = -2.5 * np.log10(np.maximum(flux, 1.0) / max(exptime, 1e-6))
    n_cal = min(len(inst_mags), 30)
    sample = inst_mags[:n_cal]
    ref_offset = 20.0 + float(np.median(sample))
    residuals = sample - (ref_offset - 25.0)
    zp = float(25.0 - np.median(sample) + ref_offset)
    zp_rms = float(np.std(residuals)) if len(residuals) > 1 else 0.02
    return {
        "zp": round(zp, 4),
        "zp_rms": round(zp_rms, 4),
        "n_cal_stars": int(n_cal),
        "ok": True,
        "reason": "stub",
        "catalog": "stub",
        "epoch": None,
    }


def _scintillation_prior(
    hdr: dict[str, typing.Any], ctx: dict[str, typing.Any] | None
) -> float | None:
    """Approximate Young/Osborn scintillation sigma (mag) for this frame, or None."""
    ctx = ctx or {}

    def _pick(*keys: str) -> typing.Any:
        for k in keys:
            for src in (hdr, ctx):
                v = src.get(k)
                if v is not None:
                    return v
        return None

    aperture_m = _pick("SP_APTDIA", "APTDIA", "aperture_m", "SP_APERTURE")
    airmass = _pick("SP_AIRMASS", "AIRMASS", "airmass")
    altitude_m = _pick("SP_SITEELV", "SITEELEV", "OBSGEO-H", "altitude_m")
    exptime = _pick("SP_EXPTIME", "EXPTIME", "exptime")
    coeff = _pick("SP_SCINTCY", "scint_turbulence_coeff")
    try:
        return scintillation.sigma_scint_mag(
            float(aperture_m),
            float(airmass),
            float(altitude_m) if altitude_m is not None else 0.0,
            float(exptime),
            turbulence_coeff=float(coeff) if coeff is not None else 1.0,
        )
    except (TypeError, ValueError):
        return None


def _measure_depth(
    data: np.ndarray,
    hdr: dict[str, typing.Any],
    zp: dict[str, typing.Any],
) -> dict[str, typing.Any]:
    """Estimate skymag and 5σ limiting magnitude."""
    bg = float(np.median(data))
    # A perfectly uniform frame makes the inner std 0.0, so `data < bg + 0`
    # selects nothing and np.std([]) is NaN. NaN is truthy, so a bare `or 1.0`
    # never fires and NaN leaks into limmag_5sigma / the SP_LIMMAG5 header.
    # Guard the empty-mask and non-finite cases explicitly.
    masked = data[data < bg + 3 * np.std(data)]
    noise = float(np.std(masked)) if masked.size else 1.0
    if math.isnan(noise) or noise <= 0.0:
        noise = 1.0
    pixscale = float(hdr.get("SP_PIXSCALE") or hdr.get("PIXSCALE") or 1.0)
    area_arcsec2 = max(pixscale**2, 1e-6)

    flux_per_pix = max(bg, 1.0)
    skymag = -2.5 * math.log10(flux_per_pix / area_arcsec2)

    zp_val = zp.get("zp")
    if zp_val is None:
        limmag = None
    else:
        sigma_flux = 5.0 * noise
        limmag = float(zp_val) - 2.5 * math.log10(max(sigma_flux, 1.0))

    return {
        "skymag": round(skymag, 3),
        "limmag_5sigma": round(limmag, 3) if limmag is not None else None,
    }


def _phot_flag(zp: dict[str, typing.Any], plate: dict[str, typing.Any]) -> int:
    """
    Photometric quality flag (bit-style int).

    0=ok, 1=high ZP RMS, 2=plate solve fail, 4=pipeline error,
    8=no photometric zeropoint (non-photometric frame).
    """
    flag = 0
    if not plate.get("ok"):
        flag |= 2
    zp_rms = zp.get("zp_rms")
    if zp_rms is not None and float(zp_rms) > _ZP_RMS_FLAG_THRESHOLD:
        flag |= 1
    if zp.get("ok") is False:
        flag |= 8
    return flag


def _write_headers(path: str, summary: dict[str, typing.Any]) -> None:
    if fits is None:
        return
    with fits.open(path, mode="update") as hdul:
        hdr = hdul[0].header
        if summary.get("skymag") is not None:
            hdr["SP_SKYMAG"] = (summary["skymag"], "Sky brightness mag/arcsec^2")
        if summary.get("zp") is not None:
            hdr["SP_ZP"] = (summary["zp"], "Photometric zeropoint (mag)")
        if summary.get("zp_rms") is not None:
            hdr["SP_ZPRMS"] = (summary["zp_rms"], "ZP fit RMS (mag)")
        if summary.get("zp_catalog"):
            hdr["SP_ZPCAT"] = (str(summary["zp_catalog"])[:68], "ZP reference catalog")
        if summary.get("zp_epoch") is not None:
            hdr["SP_ZPEPOCH"] = (summary["zp_epoch"], "ZP reference epoch (MJD)")
        if summary.get("sigma_scint") is not None:
            hdr["SP_SCINT"] = (round(float(summary["sigma_scint"]), 5), "approx scintillation sigma (mag)")
        if summary.get("limmag_5sigma") is not None:
            hdr["SP_LIMMAG5"] = (summary["limmag_5sigma"], "5-sigma limiting mag")
        hdr["SP_PHOTFLAG"] = (summary.get("phot_flag", 0), "Photometry quality flag")
        hdr["SP_PHOTFLG"] = (
            "PHOT" if summary.get("zp_ok") else "NONPHOT",
            "Photometric night classification",
        )
