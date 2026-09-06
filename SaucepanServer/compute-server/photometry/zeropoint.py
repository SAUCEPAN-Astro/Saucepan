"""Catalog-matched photometric zeropoint.

Replaces the old self-referential stub (which derived its reference offset from
the very instrumental magnitudes it then "calibrated"). Here the reference
magnitudes come from *outside* the frame:

  1. an explicit reference list handed in via ``ctx["phot_reference"]``
     (list of ``{ra, dec, mag, mag_err?}``), or
  2. a local reference-catalog file (``ctx["phot_reference_file"]`` or
     ``$PHOT_REF_CATALOG_FILE`` — JSON list or CSV with ra,dec,mag[,mag_err]), or
  3. campaign comparison stars that carry ``ref_mag`` + RA/Dec, or
  4. a live Gaia DR3 cone search (only when ``$PHOT_ALLOW_NETWORK`` is set —
     never in the test suite).

The fit is a robust inverse-variance weighted median of ``ref_mag - inst_mag``
with one 3-sigma (MAD) clip pass. It **fails closed**: fewer than
``$PHOT_MIN_CAL_STARS`` (default 5) usable matches, or a residual scatter above
``$PHOT_MAX_ZPRMS`` (default 0.15 mag), returns ``{"zp": None, "ok": False,
"reason": ...}`` and no number. The frame is then non-photometric.

Config (all env, all optional):
  PHOT_REF_CATALOG        catalog id recorded in output / SP_ZPCAT (default gaia_dr3)
  PHOT_REF_CATALOG_FILE   path to a local reference catalog
  PHOT_MIN_CAL_STARS      minimum matched calibrators (default 5)
  PHOT_MAX_ZPRMS          maximum residual MAD scatter, mag (default 0.15)
  PHOT_MATCH_RADIUS_PX    source<->reference match radius, pixels (default 3.0)
  PHOT_ALLOW_NETWORK      if set, permit the Gaia cone search
"""

from __future__ import annotations

import csv
import json
import logging
import math
import os
import typing

import numpy as np

from photometry import wcs_stars

logger = logging.getLogger(__name__)

DEFAULT_CATALOG = "gaia_dr3"
DEFAULT_MIN_CAL_STARS = 5
DEFAULT_MAX_ZPRMS = 0.15
DEFAULT_MATCH_RADIUS_PX = 3.0


# ── config helpers ────────────────────────────────────────────────────────


def _env_int(name: str, default: int) -> int:
    try:
        return int(os.environ[name])
    except (KeyError, ValueError):
        return default


def _env_float(name: str, default: float) -> float:
    try:
        return float(os.environ[name])
    except (KeyError, ValueError):
        return default


def _finite(v: typing.Any) -> bool:
    try:
        return math.isfinite(float(v))
    except (TypeError, ValueError):
        return False


def _fail(reason: str, *, catalog: str, epoch: float | None, n: int = 0, **extra: typing.Any) -> dict:
    out = {
        "zp": None,
        "zp_rms": None,
        "n_cal_stars": n,
        "ok": False,
        "reason": reason,
        "catalog": catalog,
        "epoch": epoch,
        "color_term": None,
    }
    out.update(extra)
    return out


# ── robust fit ───────────────────────────────────────────────────────────


def _weighted_median(values: np.ndarray, weights: np.ndarray) -> float:
    order = np.argsort(values)
    v = values[order]
    w = weights[order]
    cw = np.cumsum(w)
    if cw[-1] <= 0:
        return float(np.median(v))
    cutoff = 0.5 * cw[-1]
    idx = int(np.searchsorted(cw, cutoff))
    idx = min(idx, len(v) - 1)
    return float(v[idx])


def _mad_sigma(residuals: np.ndarray) -> float:
    med = float(np.median(residuals))
    return float(1.4826 * np.median(np.abs(residuals - med)))


def fit_zeropoint(
    matches: typing.Sequence[dict[str, typing.Any]],
    *,
    catalog: str = "unknown",
    epoch: float | None = None,
    color_term: float | None = None,
    min_cal_stars: int | None = None,
    max_zprms: float | None = None,
) -> dict[str, typing.Any]:
    """Robust weighted-median ZP from pre-matched ``(inst_mag, ref_mag)`` pairs.

    Each ``matches`` item: ``inst_mag`` (required), ``ref_mag`` (required),
    ``ref_err`` (optional, default 0.02), ``color`` (optional, used only if
    ``color_term`` supplied). Env overrides win over the keyword args.
    """
    min_cal = _env_int("PHOT_MIN_CAL_STARS", min_cal_stars or DEFAULT_MIN_CAL_STARS)
    max_rms = _env_float("PHOT_MAX_ZPRMS", max_zprms if max_zprms is not None else DEFAULT_MAX_ZPRMS)

    usable = [
        m for m in matches if _finite(m.get("inst_mag")) and _finite(m.get("ref_mag"))
    ]
    if len(usable) < min_cal:
        return _fail("insufficient_matches", catalog=catalog, epoch=epoch, n=len(usable))

    inst = np.array([float(m["inst_mag"]) for m in usable])
    ref = np.array([float(m["ref_mag"]) for m in usable])
    err = np.array(
        [float(m["ref_err"]) if _finite(m.get("ref_err")) and float(m["ref_err"]) > 0 else 0.02 for m in usable]
    )
    delta = ref - inst
    if color_term:
        col = np.array([float(m["color"]) if _finite(m.get("color")) else 0.0 for m in usable])
        delta = delta - float(color_term) * col

    weights = 1.0 / err**2
    zp = _weighted_median(delta, weights)
    resid = delta - zp
    zp_rms = _mad_sigma(resid)

    # One robust clip pass.
    if zp_rms > 0:
        keep = np.abs(resid - np.median(resid)) <= 3.0 * zp_rms
        if min_cal <= int(keep.sum()) < len(usable):
            delta, weights = delta[keep], weights[keep]
            usable = [u for u, k in zip(usable, keep) if k]
            zp = _weighted_median(delta, weights)
            resid = delta - zp
            zp_rms = _mad_sigma(resid)

    n = len(usable)
    if n < min_cal:
        return _fail("insufficient_matches_after_clip", catalog=catalog, epoch=epoch, n=n)
    if not math.isfinite(zp_rms) or zp_rms > max_rms:
        return _fail(
            "zp_rms_exceeds_max",
            catalog=catalog,
            epoch=epoch,
            n=n,
            zp_rms=round(zp_rms, 4) if math.isfinite(zp_rms) else None,
        )

    return {
        "zp": round(float(zp), 4),
        "zp_rms": round(float(zp_rms), 4),
        "n_cal_stars": n,
        "ok": True,
        "reason": None,
        "catalog": catalog,
        "epoch": epoch,
        "color_term": float(color_term) if color_term else None,
    }


# ── reference catalog loading ────────────────────────────────────────────


def _reference_from_file(path: str) -> list[dict[str, typing.Any]]:
    if not path or not os.path.exists(path):
        return []
    try:
        if path.lower().endswith(".json"):
            with open(path, encoding="utf-8") as fh:
                rows = json.load(fh)
            return [_ref_row(r) for r in rows if isinstance(r, dict)]
        out: list[dict[str, typing.Any]] = []
        with open(path, newline="", encoding="utf-8") as fh:
            for r in csv.DictReader(fh):
                out.append(_ref_row(r))
        return [r for r in out if r]
    except (OSError, ValueError):
        logger.warning("zeropoint: failed to read reference catalog %s", path, exc_info=True)
        return []


def _ref_row(r: dict[str, typing.Any]) -> dict[str, typing.Any]:
    ra = r.get("ra", r.get("ra_deg"))
    dec = r.get("dec", r.get("dec_deg"))
    mag = r.get("mag", r.get("ref_mag", r.get("phot_g_mean_mag")))
    err = r.get("mag_err", r.get("ref_err"))
    if not (_finite(ra) and _finite(dec) and _finite(mag)):
        return {}
    row = {"ra": float(ra), "dec": float(dec), "mag": float(mag)}
    if _finite(err):
        row["mag_err"] = float(err)
    return row


def _reference_from_comps(ctx: dict[str, typing.Any]) -> list[dict[str, typing.Any]]:
    stars = ctx.get("campaign_comp_stars")
    if not isinstance(stars, list):
        snap = ctx.get("task_snapshot") or {}
        stars = snap.get("comp_stars") or snap.get("campaign_comp_stars")
    if not isinstance(stars, list):
        return []
    out: list[dict[str, typing.Any]] = []
    for s in stars:
        if not isinstance(s, dict):
            continue
        ra = s.get("ra", s.get("ra_deg"))
        dec = s.get("dec", s.get("dec_deg"))
        mag = s.get("ref_mag", s.get("mag"))
        if _finite(ra) and _finite(dec) and _finite(mag):
            row = {"ra": float(ra), "dec": float(dec), "mag": float(mag)}
            if _finite(s.get("ref_err")):
                row["mag_err"] = float(s["ref_err"])
            out.append(row)
    return out


def _reference_from_gaia(
    ra: float, dec: float, radius_deg: float
) -> list[dict[str, typing.Any]]:  # pragma: no cover - network path
    if not os.environ.get("PHOT_ALLOW_NETWORK"):
        return []
    try:
        from astroquery.gaia import Gaia

        job = Gaia.launch_job(
            "SELECT TOP 500 ra, dec, phot_g_mean_mag "
            "FROM gaiadr3.gaia_source "
            f"WHERE 1=CONTAINS(POINT('ICRS', ra, dec), "
            f"CIRCLE('ICRS', {ra}, {dec}, {radius_deg})) "
            "AND phot_g_mean_mag IS NOT NULL"
        )
        rows = job.get_results()
        return [
            {"ra": float(r["ra"]), "dec": float(r["dec"]), "mag": float(r["phot_g_mean_mag"])}
            for r in rows
        ]
    except Exception:
        logger.warning("zeropoint: Gaia cone search failed", exc_info=True)
        return []


def load_reference(
    ctx: dict[str, typing.Any],
    plate: dict[str, typing.Any],
    *,
    fov_deg: float = 0.5,
) -> tuple[list[dict[str, typing.Any]], str]:
    """Return ``(reference_stars, source_label)``; empty list means no reference."""
    catalog = os.environ.get("PHOT_REF_CATALOG") or ctx.get("phot_catalog") or DEFAULT_CATALOG

    inline = ctx.get("phot_reference")
    if isinstance(inline, list) and inline:
        rows = [_ref_row(r) for r in inline if isinstance(r, dict)]
        rows = [r for r in rows if r]
        if rows:
            return rows, f"{catalog}:inline"

    path = ctx.get("phot_reference_file") or os.environ.get("PHOT_REF_CATALOG_FILE")
    if path:
        rows = _reference_from_file(str(path))
        if rows:
            return rows, f"{catalog}:file"

    comps = _reference_from_comps(ctx)
    if comps:
        return comps, "campaign_comp_stars"

    ra, dec = plate.get("ra"), plate.get("dec")
    if _finite(ra) and _finite(dec):
        rows = _reference_from_gaia(float(ra), float(dec), fov_deg)
        if rows:
            return rows, catalog

    return [], catalog


# ── matching + frame-level entry point ──────────────────────────────────


def _match_sources(
    refs: list[dict[str, typing.Any]],
    wcs: typing.Any,
    sources: dict[str, typing.Any],
    match_radius_px: float,
    exptime: float,
) -> list[dict[str, typing.Any]]:
    xs = np.asarray(sources.get("x", []), dtype=float)
    ys = np.asarray(sources.get("y", []), dtype=float)
    flux = np.asarray(sources.get("flux", []), dtype=float)
    if len(xs) == 0 or len(xs) != len(flux):
        return []

    projected = wcs_stars.project_comp_stars(
        [{"id": i, "ra": r["ra"], "dec": r["dec"], **r} for i, r in enumerate(refs)],
        wcs,
        None,
    )
    matches: list[dict[str, typing.Any]] = []
    used: set[int] = set()
    for p in projected:
        d2 = (xs - p["x"]) ** 2 + (ys - p["y"]) ** 2
        j = int(np.argmin(d2))
        if j in used or d2[j] > match_radius_px**2:
            continue
        if flux[j] <= 0:
            continue
        used.add(j)
        matches.append(
            {
                "inst_mag": -2.5 * math.log10(flux[j] / exptime),
                "ref_mag": p.get("mag"),
                "ref_err": p.get("mag_err"),
                "color": p.get("color"),
                "sep_px": math.sqrt(float(d2[j])),
            }
        )
    return matches


def _epoch_from_header(hdr: dict[str, typing.Any]) -> float | None:
    for key in ("MJD-OBS", "SP_MJD", "MJD"):
        if _finite(hdr.get(key)):
            return float(hdr[key])
    return None


def zeropoint_for_frame(
    sources: dict[str, typing.Any],
    plate: dict[str, typing.Any],
    hdr: dict[str, typing.Any],
    ctx: dict[str, typing.Any],
) -> dict[str, typing.Any]:
    """Catalog-matched ZP for one frame. Fail-closed; never raises."""
    catalog_label = os.environ.get("PHOT_REF_CATALOG") or ctx.get("phot_catalog") or DEFAULT_CATALOG
    epoch = _epoch_from_header(hdr)
    try:
        if not plate.get("ok"):
            return _fail("plate_solve_failed", catalog=catalog_label, epoch=epoch)

        refs, source_label = load_reference(ctx, plate)
        if not refs:
            return _fail("no_reference_catalog", catalog=catalog_label, epoch=epoch)

        wcs = wcs_stars.wcs_from_header(hdr)
        if wcs is None:
            return _fail("no_wcs_for_match", catalog=source_label, epoch=epoch)

        exptime = float(hdr.get("SP_EXPTIME") or hdr.get("EXPTIME") or 1.0) or 1.0
        radius = _env_float("PHOT_MATCH_RADIUS_PX", DEFAULT_MATCH_RADIUS_PX)
        matches = _match_sources(refs, wcs, sources, radius, exptime)

        return fit_zeropoint(
            matches,
            catalog=source_label,
            epoch=epoch,
            color_term=ctx.get("color_term"),
        )
    except Exception:
        logger.exception("zeropoint_for_frame failed")
        return _fail("zeropoint_error", catalog=catalog_label, epoch=epoch)
