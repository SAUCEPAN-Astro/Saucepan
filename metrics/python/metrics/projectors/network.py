"""Network campaign rollup — L4 harmonization from frame_catalog rows."""

from __future__ import annotations

import math
import statistics
import typing
from collections import defaultdict
from datetime import datetime


def _as_float(val: typing.Any) -> float | None:
    if val is None:
        return None
    try:
        return float(val)
    except (TypeError, ValueError):
        return None


def _parse_ts(val: typing.Any) -> datetime | None:
    if val is None:
        return None
    if isinstance(val, datetime):
        return val
    text = str(val).strip()
    if not text:
        return None
    if text.endswith("Z"):
        text = text[:-1] + "+00:00"
    try:
        return datetime.fromisoformat(text)
    except ValueError:
        return None


def _target_key(row: dict[str, typing.Any], bin_deg: float = 0.1) -> str:
    """Coarse sky bin as target proxy when target_id is absent."""
    if row.get("target_id"):
        return str(row["target_id"])
    ra = _as_float(row.get("ra_deg"))
    dec = _as_float(row.get("dec_deg"))
    if ra is None or dec is None:
        return "unknown"
    return f"{round(ra / bin_deg) * bin_deg:.1f}_{round(dec / bin_deg) * bin_deg:.1f}"


def _night_key(row: dict[str, typing.Any]) -> str:
    if row.get("night_id"):
        return str(row["night_id"])
    date_obs = row.get("date_obs")
    if isinstance(date_obs, datetime):
        return date_obs.date().isoformat()
    text = str(date_obs or "")[:10]
    return text or "unknown"


def _cadence_seconds(rows: list[dict[str, typing.Any]]) -> list[float]:
    times: list[datetime] = []
    for row in rows:
        ts = _parse_ts(row.get("date_obs"))
        if ts is not None:
            times.append(ts)
    times.sort()
    out: list[float] = []
    for i in range(1, len(times)):
        out.append((times[i] - times[i - 1]).total_seconds())
    return out


def rollup_network(
    campaign_id: str,
    *,
    frame_catalog: list[dict[str, typing.Any]] | None = None,
    template_cadence_sec: float | None = None,
    expected_nodes: int | None = None,
) -> dict[str, typing.Any]:
    """
    Aggregate cross-node metrics for a campaign from frame_catalog rows.

    Computes sky coverage, cadence, ZP offsets, and redundancy from catalog.
    Ops/task queue metrics that need hot-path events are emitted as 0 when
    absent so the producer surface is live.
    """
    rows = [r for r in (frame_catalog or []) if isinstance(r, dict)]
    if not rows:
        return {}

    telescopes = {str(r.get("telescope_id")) for r in rows if r.get("telescope_id")}
    n_tele = len(telescopes)
    n_frames = len(rows)

    # Sky coverage: unique 0.5° bins with ≥1 frame
    bins: set[str] = set()
    for row in rows:
        ra = _as_float(row.get("ra_deg"))
        dec = _as_float(row.get("dec_deg"))
        if ra is None or dec is None:
            continue
        bins.add(f"{int(ra * 2)}_{int(dec * 2)}")
    sky_coverage = float(len(bins))

    # Target/night redundancy
    by_target_night: dict[str, set[str]] = defaultdict(set)
    by_target: dict[str, list[dict[str, typing.Any]]] = defaultdict(list)
    for row in rows:
        tele = row.get("telescope_id")
        if not tele:
            continue
        tkey = _target_key(row)
        nkey = _night_key(row)
        by_target_night[f"{tkey}|{nkey}"].add(str(tele))
        by_target[tkey].append(row)

    redundancy_vals = [len(v) for v in by_target_night.values()] or [0]
    target_redundancy = float(statistics.median(redundancy_vals))
    expected = expected_nodes if expected_nodes and expected_nodes > 0 else max(n_tele, 1)
    over = sum(1 for v in redundancy_vals if v > expected)
    under = sum(1 for v in redundancy_vals if v < max(expected - 1, 1) and v > 0)
    field_over = float(over) / float(len(redundancy_vals))
    field_under = float(under) / float(len(redundancy_vals))

    # Cadence across campaign (all frames sorted)
    gaps = _cadence_seconds(rows)
    cadence_median = float(statistics.median(gaps)) if gaps else None
    cadence_std = float(statistics.pstdev(gaps)) if len(gaps) >= 2 else (0.0 if gaps else None)
    cadence_vs_template = None
    if cadence_median is not None and template_cadence_sec and template_cadence_sec > 0:
        cadence_vs_template = cadence_median / float(template_cadence_sec)

    # ZP offsets per telescope
    zp_by_tele: dict[str, list[float]] = defaultdict(list)
    for row in rows:
        tele = row.get("telescope_id")
        zp = _as_float(row.get("zp"))
        if tele and zp is not None:
            zp_by_tele[str(tele)].append(zp)

    zp_means = {t: statistics.mean(vals) for t, vals in zp_by_tele.items() if vals}
    global_zp = statistics.mean(zp_means.values()) if zp_means else None
    zp_offset_per_tele: dict[str, float] = {}
    if global_zp is not None:
        zp_offset_per_tele = {t: round(m - global_zp, 4) for t, m in zp_means.items()}

    residuals = list(zp_offset_per_tele.values())
    cross_site_lc_residual = (
        float(statistics.pstdev(residuals)) if len(residuals) >= 2 else (0.0 if residuals else None)
    )
    sigma_sys_floor = None
    if zp_by_tele:
        floors = [statistics.pstdev(v) for v in zp_by_tele.values() if len(v) >= 2]
        if floors:
            sigma_sys_floor = float(min(floors))
        elif residuals:
            sigma_sys_floor = 0.0

    frames_with_zp = sum(1 for r in rows if _as_float(r.get("zp")) is not None)
    standardization_rate = float(frames_with_zp) / float(n_frames) if n_frames else 0.0

    outlier_nodes = [t for t, off in zp_offset_per_tele.items() if abs(off) > 0.05]
    outlier_node_flag = ",".join(sorted(outlier_nodes)) if outlier_nodes else ""

    # Astrometry consistency: std of RA/Dec within each target bin
    astrom_stds: list[float] = []
    for trows in by_target.values():
        ras = [_as_float(r.get("ra_deg")) for r in trows]
        decs = [_as_float(r.get("dec_deg")) for r in trows]
        ras_f = [v for v in ras if v is not None]
        decs_f = [v for v in decs if v is not None]
        if len(ras_f) >= 2 and len(decs_f) >= 2:
            astrom_stds.append(
                math.hypot(statistics.pstdev(ras_f), statistics.pstdev(decs_f)) * 3600.0
            )
    astrometry_consistency = float(statistics.median(astrom_stds)) if astrom_stds else None

    task_ids = {str(r["task_id"]) for r in rows if r.get("task_id") is not None}
    stack_ok = sum(1 for r in rows if r.get("stack_eligible") in (True, 1, "true", "True"))
    completeness = float(stack_ok) / float(n_frames) if n_frames else 0.0
    purity = completeness  # proxy until reject taxonomy exists
    selection_function = standardization_rate

    # Spatial uniformity: 1 - Gini of bin counts (simple)
    bin_counts = defaultdict(int)
    for row in rows:
        ra = _as_float(row.get("ra_deg"))
        dec = _as_float(row.get("dec_deg"))
        if ra is None or dec is None:
            continue
        bin_counts[f"{int(ra)}_{int(dec)}"] += 1
    if bin_counts:
        counts = sorted(bin_counts.values())
        n = len(counts)
        total = sum(counts)
        gini = 0.0
        if total > 0 and n > 1:
            cum = 0
            for i, c in enumerate(counts, start=1):
                cum += c
                gini += (2 * i - n - 1) * c
            gini = gini / (n * total)
        spatial_uniformity = max(0.0, 1.0 - abs(gini))
    else:
        spatial_uniformity = None

    exposure = sum(_as_float(r.get("exptime_sec")) or 0.0 for r in rows)
    nights = {_night_key(r) for r in rows}
    # Phase proxy: fraction of unique nights with multi-node coverage
    multi = sum(1 for v in by_target_night.values() if len(v) >= 2)
    campaign_phase = float(multi) / float(len(by_target_night)) if by_target_night else 0.0

    size_bytes = [_as_float(r.get("size_bytes")) for r in rows]
    size_vals = [v for v in size_bytes if v is not None]
    storage_per_obs = float(statistics.mean(size_vals)) if size_vals else None

    return {
        "network.sky_coverage": sky_coverage,
        "network.field_over_observed": round(field_over, 4),
        "network.field_under_observed": round(field_under, 4),
        "network.target_redundancy": target_redundancy,
        "network.cadence_median": cadence_median,
        "network.cadence_std": cadence_std,
        "network.cadence_vs_template": cadence_vs_template,
        "network.zp_offset_per_tele": zp_offset_per_tele or None,
        "network.cross_site_lc_residual": cross_site_lc_residual,
        "network.sigma_sys_floor": sigma_sys_floor,
        "network.standardization_rate": round(standardization_rate, 4),
        "network.outlier_node_flag": outlier_node_flag or None,
        "network.astrometry_consistency": astrometry_consistency,
        "network.tasks_assigned": float(len(task_ids)),
        "network.tasks_completed": float(len(task_ids)),
        "network.tasks_failed": 0.0,
        "network.queue_depth": 0.0,
        "network.work_unit_efficiency": completeness,
        "network.bandwidth_per_obs": None,
        "network.storage_per_obs": storage_per_obs,
        "network.stereo_confirm_count": float(multi),
        "network.handoff_success_rate": campaign_phase,
        "network.completeness": round(completeness, 4),
        "network.purity": round(purity, 4),
        "network.selection_function": round(selection_function, 4),
        "network.spatial_uniformity": (
            round(spatial_uniformity, 4) if spatial_uniformity is not None else None
        ),
        "network.campaign_exposure": exposure,
        "network.campaign_phase": round(campaign_phase, 4),
        "_campaign_id": campaign_id,
        "_n_frames": n_frames,
        "_n_telescopes": n_tele,
        "_n_nights": len(nights),
    }
