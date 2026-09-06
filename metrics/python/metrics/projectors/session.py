"""Session night rollup — L1 frame metrics → L2 session.* aggregates."""

from __future__ import annotations

import functools
import pathlib
import statistics
import typing

import yaml

from metrics._contracts import contract_path

_THRESHOLDS_PATH = contract_path("session_thresholds.yaml")


@functools.lru_cache(maxsize=1)
def load_session_thresholds(path: pathlib.Path | None = None) -> dict[str, typing.Any]:
    cfg_path = path or _THRESHOLDS_PATH
    with cfg_path.open(encoding="utf-8") as fh:
        return yaml.safe_load(fh) or {}


def _metric_values(frames: list[dict[str, typing.Any]], *keys: str) -> list[float]:
    out: list[float] = []
    for frame in frames:
        for key in keys:
            val = frame.get(key)
            if val is None:
                continue
            try:
                out.append(float(val))
            except (TypeError, ValueError):
                continue
            break
    return out


def _classify_phot_class(
    thresholds: dict[str, typing.Any],
    *,
    zp_drift: float | None,
) -> str:
    cfg = thresholds.get("session.phot_class") or {}
    clear_max = float(cfg.get("clear_max_drift_mag", 0.0))
    thin_max = float(cfg.get("thin_cloud_max_drift_mag", 0.3))
    if zp_drift is None:
        return "UNKNOWN"
    if zp_drift <= clear_max:
        return "PHOT"
    if zp_drift <= thin_max:
        return "NONPHOT"
    return "REJECT"


def _fit_extinction(frames: list[dict[str, typing.Any]]) -> float | None:
    """
    Linear fit ZP = zp0 - k * airmass → return extinction coeff k (#27).

    Needs ≥2 frames with both zp and airmass.
    """
    pairs: list[tuple[float, float]] = []
    for row in frames:
        zp = None
        am = None
        for key in ("zp", "frame.zp"):
            if row.get(key) is not None:
                try:
                    zp = float(row[key])
                    break
                except (TypeError, ValueError):
                    pass
        for key in ("airmass", "frame.airmass"):
            if row.get(key) is not None:
                try:
                    am = float(row[key])
                    break
                except (TypeError, ValueError):
                    pass
        if zp is not None and am is not None:
            pairs.append((am, zp))
    if len(pairs) < 2:
        return None
    n = len(pairs)
    mean_x = sum(p[0] for p in pairs) / n
    mean_y = sum(p[1] for p in pairs) / n
    var_x = sum((p[0] - mean_x) ** 2 for p in pairs)
    if var_x <= 0:
        return None
    cov = sum((p[0] - mean_x) * (p[1] - mean_y) for p in pairs)
    slope = cov / var_x  # dZP/dX
    # Extinction k where ZP decreases with airmass: ZP = zp0 - k*X → k = -slope
    return float(-slope)


def rollup_night(
    telescope_id: str,
    night_id: str,
    *,
    frames: list[dict[str, typing.Any]] | None = None,
) -> dict[str, typing.Any]:
    """
    Aggregate one telescope/night from frame-level metric dicts.

    Each frame dict may use bare keys (``fwhm_arcsec``) or prefixed keys
    (``frame.fwhm_arcsec``). Thresholds come from ``session_thresholds.yaml``.
    """
    thresholds = load_session_thresholds()
    frame_rows = list(frames or [])

    n_frames = len(frame_rows)
    exptime_total = sum(
        float(row.get("exptime_sec") or row.get("frame.exptime_sec") or 0.0) for row in frame_rows
    )

    fwhm_vals = _metric_values(frame_rows, "fwhm_arcsec", "frame.fwhm_arcsec")
    airmass_vals = _metric_values(frame_rows, "airmass", "frame.airmass")
    zp_vals = _metric_values(frame_rows, "zp", "frame.zp")

    rejected = sum(
        1 for row in frame_rows if row.get("rejected") or row.get("stack_eligible") is False
    )
    plate_ok = sum(
        1
        for row in frame_rows
        if row.get("plate_solve_ok") is True or row.get("frame.plate_solve_ok") is True
    )
    plate_attempts = sum(
        1
        for row in frame_rows
        if row.get("plate_solve_ok") is not None or row.get("frame.plate_solve_ok") is not None
    )

    fwhm_rms_pct: float | None = None
    if len(fwhm_vals) >= 2:
        mean_fwhm = statistics.mean(fwhm_vals)
        if mean_fwhm > 0:
            fwhm_rms_pct = (statistics.pstdev(fwhm_vals) / mean_fwhm) * 100.0

    airmass_range: float | None = None
    if len(airmass_vals) >= 2:
        airmass_range = max(airmass_vals) - min(airmass_vals)
    elif len(airmass_vals) == 1:
        airmass_range = 0.0

    zp_median: float | None = statistics.median(zp_vals) if zp_vals else None
    zp_drift: float | None = None
    if len(zp_vals) >= 2:
        zp_drift = max(zp_vals) - min(zp_vals)
    elif len(zp_vals) == 1:
        zp_drift = 0.0

    phot_class = _classify_phot_class(thresholds, zp_drift=zp_drift)

    extinction = _fit_extinction(frame_rows)

    out: dict[str, typing.Any] = {
        "session.night_id": night_id,
        "session.frames": n_frames,
        "session.exptime_total": exptime_total,
        "session.phot_class": phot_class,
        "session.reject_fraction": (rejected / n_frames) if n_frames else 0.0,
    }
    if extinction is not None:
        out["frame.extinction_coeff"] = round(extinction, 4)
    if fwhm_rms_pct is not None:
        out["session.fwhm_rms_pct"] = round(fwhm_rms_pct, 3)
    if airmass_range is not None:
        out["session.airmass_range"] = round(airmass_range, 4)
    if zp_median is not None:
        out["session.zp_median"] = round(zp_median, 4)
    if zp_drift is not None:
        out["session.zp_drift"] = round(zp_drift, 4)
    if plate_attempts:
        out["session.plate_solve_success_rate"] = round(plate_ok / plate_attempts, 4)

    comp_vals = _metric_values(
        frame_rows, "comp_rms_mag", "session.comp_rms_mag", "lp.comp_rms_mag"
    )
    if comp_vals:
        out["session.comp_rms_mag"] = round(statistics.median(comp_vals), 5)

    for optional_key, frame_keys in (
        ("session.moon_sep_min", ("moon_sep_min", "frame.moon_sep_min")),
        ("session.moon_illum", ("moon_illum", "frame.moon_illum")),
        ("session.clear_sky_hours", ("clear_sky_hours",)),
        ("session.check_star_kc_scatter", ("check_star_kc_scatter", "lp.check_minus_comp")),
    ):
        vals = _metric_values(frame_rows, *frame_keys)
        if vals:
            out[optional_key] = round(
                min(vals) if optional_key == "session.moon_sep_min" else statistics.median(vals),
                4,
            )

    return out
