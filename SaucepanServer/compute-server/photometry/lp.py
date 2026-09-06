"""LP alpha v1 — aperture photometry stub for campaign comp/check stars."""

from __future__ import annotations

import logging
import math
import typing

import numpy as np

from photometry import uncertainty, wcs_stars

logger = logging.getLogger(__name__)

try:
    from astropy.io import fits
except ImportError:  # pragma: no cover
    fits = None  # type: ignore[assignment]


def run_lp(
    ctx: dict[str, typing.Any],
    photometry_result: dict[str, typing.Any] | None = None,
    *,
    fits_path: str | None = None,
) -> dict[str, typing.Any]:
    """
    Aperture photometry stub for campaign comparison stars.

    Reads comp-star list from ctx (``campaign_comp_stars`` or ``task_snapshot``).
    """
    path = fits_path or ctx.get("staged_path")
    stars = _comp_stars_from_ctx(ctx)
    result: dict[str, typing.Any] = {
        "status": "ok",
        "n_comp_stars": len(stars),
        "aperture_radius_px": 5.0,
    }
    if not path or not stars:
        result["status"] = "skipped"
        result["reason"] = "no_path_or_comp_stars"
        return result

    try:
        data, header = _load_data_and_header(path)
        zp = (photometry_result or {}).get("zp") or 25.0

        # Bridge RA/Dec comp stars onto this frame's pixel grid (#203). Stars
        # that already carry pixel x/y fall through unchanged (legacy path).
        wcs = None
        if any(s.get("ra") is not None and s.get("dec") is not None for s in stars):
            wcs = wcs_stars.wcs_from_header(header) or wcs_stars.wcs_from_header(ctx.get("wcs_header"))
        if wcs is not None:
            stars = wcs_stars.project_comp_stars(stars, wcs, data.shape)
            result["comp_star_projection"] = "wcs"

        gain = _num(ctx.get("gain") or header.get("SP_GAIN") or header.get("GAIN"), 1.0)
        rdnoise = _num(ctx.get("rdnoise") or header.get("SP_RDNOISE") or header.get("RDNOISE"), 0.0)
        measurements = [_measure_star(data, star, gain=gain, rdnoise=rdnoise) for star in stars]
        comps = [m for m in measurements if m.get("role") == "comp"]
        checks = [m for m in measurements if m.get("role") == "check"]

        if comps:
            primary = comps[0]
            result.update(
                {
                    "lp.inst_mag": primary["inst_mag"],
                    "lp.comp_id": primary["id"],
                    "lp.comp_inst_mag": primary["inst_mag"],
                    "lp.comp_ref_mag": primary.get("ref_mag"),
                    "lp.comp_ref_err": primary.get("ref_err"),
                    "lp.aperture_radius": result["aperture_radius_px"],
                }
            )
            if primary.get("mag_err") is not None:
                result["lp.comp_inst_mag_err"] = round(primary["mag_err"], 4)

            # Real per-measurement error on the differential magnitude (#203):
            # target photon/sky/read error combined with the comp ensemble.
            target_err = primary.get("mag_err")
            ens_err = uncertainty.differential_mag_err(
                target_err, [m.get("mag_err") for m in comps[1:]]
            ) if len(comps) > 1 else target_err
            if ens_err is not None:
                result["lp.mag_err"] = round(ens_err, 4)

            # ZP implied by comps that carry a reference magnitude.
            zp_terms = [
                (m["ref_mag"] - m["inst_mag"], m.get("mag_err") or m.get("ref_err") or 0.05)
                for m in comps
                if m.get("ref_mag") is not None
            ]
            if zp_terms:
                w = np.array([1.0 / max(float(e), 1e-3) ** 2 for _, e in zp_terms])
                vals = np.array([v for v, _ in zp_terms])
                result["lp.zp_from_comps"] = round(float(np.sum(w * vals) / np.sum(w)), 4)
                if len(zp_terms) > 1:
                    result["lp.comp_rms_mag"] = round(float(np.std(vals)), 4)

        if checks and comps:
            check = checks[0]
            comp = comps[0]
            result.update(
                {
                    "lp.check_id": check["id"],
                    "lp.check_inst_mag": check["inst_mag"],
                    "lp.check_minus_comp": round(check["inst_mag"] - comp["inst_mag"], 4),
                }
            )

        if measurements:
            # Historical stub: scatter of instrumental mags (not JC std mag).
            result["lp.inst_mag_scatter"] = round(
                float(np.std([m["inst_mag"] for m in measurements])), 4
            )

        result["lp.delta_mag"] = round(float(result.get("lp.inst_mag", zp)) - float(zp), 4)

        # Ensemble weight / aperture correction: lightweight first cut (#422).
        # Full inverse-variance ensemble + growth-curve APERCOR remain follow-ups.
        if comps:
            weights = []
            for m in comps:
                ref_err = m.get("ref_err")
                try:
                    e = float(ref_err) if ref_err is not None else 0.05
                except (TypeError, ValueError):
                    e = 0.05
                weights.append(1.0 / max(e, 1e-3) ** 2)
            wsum = sum(weights) or 1.0
            result["lp.ensemble_weight"] = round(weights[0] / wsum, 4)
        fwhm = ctx.get("fwhm_px") or ctx.get("sp_fwhm")
        try:
            fwhm_f = float(fwhm) if fwhm is not None else None
        except (TypeError, ValueError):
            fwhm_f = None
        if fwhm_f and fwhm_f > 0:
            # Crude growth-curve placeholder: correction → 0 as aperture ≫ FWHM.
            r = float(result["aperture_radius_px"])
            result["lp.aperture_correction"] = round(max(0.0, 0.1 * (fwhm_f / max(r, 1e-3))), 4)
        else:
            result["lp.aperture_correction"] = 0.0

        try:
            from photometry.table import row_from_lp

            result["photometry_table_row"] = row_from_lp(result, ctx=ctx)
        except Exception:
            logger.exception("photometry table row build failed")

        # Optional #419 path: apply per-telescope JC colour-term profile.
        profile = ctx.get("transform_profile")
        color_index = ctx.get("color_index")
        if profile is not None and color_index is not None and "lp.inst_mag" in result:
            from photometry.transform import apply_transform, load_profile

            if isinstance(profile, str):
                profile = load_profile(profile)
            applied = apply_transform(
                float(result["lp.inst_mag"]),
                color_index=float(color_index),
                profile=profile,
                airmass=ctx.get("airmass"),
                zp_override=zp,
                mag_err=result.get("lp.mag_err"),
            )
            result["lp.std_mag"] = applied["lp.std_mag"]
            result["lp.color_term"] = applied["lp.color_term"]
            result["lp.transform_coeff"] = applied["lp.transform_coeff"]
            result["lp.transform_applied"] = True
            result["transform"] = applied

    except Exception as exc:
        logger.exception("LP photometry stub failed")
        result["status"] = "error"
        result["error"] = str(exc)

    return result


def _comp_stars_from_ctx(ctx: dict[str, typing.Any]) -> list[dict[str, typing.Any]]:
    stars = ctx.get("campaign_comp_stars")
    if isinstance(stars, list) and stars:
        return [_normalize_star(s) for s in stars]

    snap = ctx.get("task_snapshot") or {}
    stars = snap.get("comp_stars") or snap.get("campaign_comp_stars")
    if isinstance(stars, list):
        return [_normalize_star(s) for s in stars]
    return []


def _num(value: typing.Any, default: float) -> float:
    try:
        v = float(value)
        return v if math.isfinite(v) else default
    except (TypeError, ValueError):
        return default


def _normalize_star(raw: dict[str, typing.Any]) -> dict[str, typing.Any]:
    role = str(raw.get("role") or "comp").lower()
    star: dict[str, typing.Any] = {
        "id": raw.get("id") or raw.get("name") or "comp",
        "ref_mag": raw.get("ref_mag") if raw.get("ref_mag") is not None else raw.get("mag"),
        "ref_err": raw.get("ref_err"),
        "role": role if role in {"comp", "check"} else "comp",
    }
    # Sky coordinates (campaign packs) take priority; pixel x/y kept as the
    # legacy fallback for fixtures that predate WCS projection.
    ra = raw.get("ra") if raw.get("ra") is not None else raw.get("ra_deg")
    dec = raw.get("dec") if raw.get("dec") is not None else raw.get("dec_deg")
    if ra is not None and dec is not None:
        star["ra"] = float(ra)
        star["dec"] = float(dec)
    x = raw.get("x") if raw.get("x") is not None else raw.get("x_pix")
    y = raw.get("y") if raw.get("y") is not None else raw.get("y_pix")
    if x is not None or y is not None:
        star["x"] = float(x or 0)
        star["y"] = float(y or 0)
    elif "ra" not in star:
        star["x"] = 0.0
        star["y"] = 0.0
    return star


def _load_data(path: str) -> np.ndarray:
    return _load_data_and_header(path)[0]


def _load_data_and_header(path: str) -> tuple[np.ndarray, dict[str, typing.Any]]:
    if fits is None:
        raise ImportError("astropy required for LP photometry")
    with fits.open(path, memmap=True) as hdul:
        data = np.asarray(hdul[0].data, dtype=np.float32)
        header = dict(hdul[0].header)
    return data, header


def _measure_star(
    data: np.ndarray,
    star: dict[str, typing.Any],
    *,
    gain: float = 1.0,
    rdnoise: float = 0.0,
) -> dict[str, typing.Any]:
    """Circular aperture sum + per-measurement magnitude error (#203)."""
    r = 5.0
    x0, y0 = int(star["x"]), int(star["y"])
    h, w = data.shape
    mag_err: float | None = None
    if x0 < 0 or y0 < 0 or x0 >= w or y0 >= h:
        flux = 1.0
        npix = math.pi * r * r
        sky = 0.0
        sky_err = 0.0
    else:
        yy, xx = np.ogrid[:h, :w]
        mask = (xx - x0) ** 2 + (yy - y0) ** 2 <= r**2
        sky = float(np.median(data))
        npix = float(np.count_nonzero(mask))
        flux = float(np.sum(np.maximum(data[mask] - sky, 1.0)))
        # Sky-estimate error from the annulus / full-frame MAD.
        sky_err = float(1.4826 * np.median(np.abs(data - sky))) / max(math.sqrt(data.size), 1.0)
        mag_err = uncertainty.mag_err_from_flux(
            flux, npix=npix, sky=max(sky, 0.0), gain=gain, rdnoise=rdnoise, sky_err=sky_err
        )

    inst_mag = -2.5 * math.log10(max(flux, 1.0))
    out = {**star, "flux": flux, "inst_mag": round(inst_mag, 4), "npix": npix, "sky": sky}
    if mag_err is not None:
        out["mag_err"] = mag_err
    return out
