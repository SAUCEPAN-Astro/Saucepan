"""Producer: node.* profile — static telescope snapshot + 24h cached atmosphere.

Refresh cadence (#22): static fields from telescopes DB / upload metadata;
dynamic atmosphere fields from ``node_cache`` refreshed at most every 24h
(client or apiserver stamps ``node_cache.cached_at``).
"""

from __future__ import annotations

import typing
from datetime import datetime, timezone

from metrics.observation import EntityContext

_STATIC_FIELDS: dict[str, tuple[str, ...]] = {
    "node.tele_id": ("telescope_id", "tele_id"),
    "node.aperture_mm": ("aperture_mm",),
    "node.focal_mm": ("focal_length_mm", "focal_mm"),
    "node.pixel_um": ("pixel_size_um", "pixel_um"),
    "node.pixscale": ("pixscale", "pixel_scale"),
    "node.fov_w_arcmin": ("fov_w_arcmin", "fov_width_arcmin"),
    "node.fov_h_arcmin": ("fov_h_arcmin", "fov_height_arcmin"),
    "node.site_lat": ("site_latitude", "site_lat"),
    "node.site_lon": ("site_longitude", "site_lon"),
    "node.mount_type": ("mount_type",),
    "node.filters": ("available_filters", "filters"),
    "node.quality_tier": ("quality_tier",),
    "node.slew_rate": ("slew_rate",),
    "node.median_seeing": ("median_seeing", "median_seeing_arcsec"),
    "node.pipeline_ver": ("pipeline_ver", "pipeline_version"),
    "node.last_cal_bias_at": ("last_cal_bias_at",),
    "node.last_cal_dark_at": ("last_cal_dark_at",),
    "node.last_cal_flat_at": ("last_cal_flat_at",),
    "node.qe_curve": ("qe_curve", "qe"),
    "node.linearity_saturation": ("linearity_saturation",),
    "node.cosmetic_map": ("cosmetic_map",),
    "node.feature_vector": ("feature_vector",),
    "node.pointing_accuracy": ("pointing_accuracy",),
}

_DYNAMIC_FIELDS: dict[str, tuple[str, ...]] = {
    "node.bortle": ("bortle",),
    "node.skymag_median": ("skymag_median",),
    "node.seeing_p50": ("seeing_p50",),
    "node.seeing_p90": ("seeing_p90",),
    "node.uptime_hours": ("uptime_hours",),
    "node.false_positive_rate": ("false_positive_rate",),
    "node.trust_rank": ("trust_rank",),
}

_CACHE_MAX_AGE_SEC = 24 * 3600


def _pick(snap: dict[str, typing.Any], keys: tuple[str, ...]) -> typing.Any:
    for key in keys:
        val = snap.get(key)
        if val is not None:
            return val
    return None


def _cache_fresh(cache: dict[str, typing.Any]) -> bool:
    raw = cache.get("cached_at") or cache.get("updated_at")
    if not raw:
        return bool(cache)  # treat present cache as usable if unstamped
    try:
        text = str(raw).replace("Z", "+00:00")
        ts = datetime.fromisoformat(text)
        if ts.tzinfo is None:
            ts = ts.replace(tzinfo=timezone.utc)
        age = (datetime.now(timezone.utc) - ts).total_seconds()
        return age <= _CACHE_MAX_AGE_SEC
    except ValueError:
        return True


def produce(ctx: EntityContext) -> dict[str, typing.Any]:
    snap = dict(ctx.get("telescope_snapshot") or {})
    cache = dict(ctx.get("node_cache") or {})
    rep = dict(ctx.get("reputation_stats") or {})
    out: dict[str, typing.Any] = {}

    for metric_id, keys in _STATIC_FIELDS.items():
        val = _pick(snap, keys)
        if val is not None:
            out[metric_id] = val

    if _cache_fresh(cache):
        for metric_id, keys in _DYNAMIC_FIELDS.items():
            val = _pick(cache, keys)
            if val is not None:
                out[metric_id] = val

    # Seeing fallbacks from static median when cache empty
    if "node.seeing_p50" not in out and snap.get("median_seeing_arcsec") is not None:
        out["node.seeing_p50"] = snap["median_seeing_arcsec"]
    if "node.median_seeing" not in out and snap.get("median_seeing_arcsec") is not None:
        out["node.median_seeing"] = snap["median_seeing_arcsec"]

    if rep.get("reliability_score") is not None:
        out["node.reliability_score"] = rep["reliability_score"]
    if rep.get("false_positive_rate") is not None and "node.false_positive_rate" not in out:
        out["node.false_positive_rate"] = rep["false_positive_rate"]

    node_id = ctx.get("node_id") or snap.get("node_id") or ctx.get("telescope_id")
    if node_id:
        out["node.node_id"] = node_id
    tele = ctx.get("telescope_id") or snap.get("telescope_id")
    if tele and "node.tele_id" not in out:
        out["node.tele_id"] = tele

    clock = ctx.get("clock_source") or snap.get("clock_source")
    if clock:
        out["node.clock_source"] = clock

    return out
