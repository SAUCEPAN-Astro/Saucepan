"""
Per-frame points and reputation EMA helpers (pure math, no DB).
"""

from __future__ import annotations

import datetime
import math
import typing

from grading import constants, dimensions, schema


def ema_update(
    previous: float | None,
    sample: float,
    alpha: float,
) -> float:
    """Exponential moving average; seed with first sample when no history."""
    sample_value = dimensions._safe_float(sample) or 0.0
    alpha_value = dimensions._safe_float(alpha) or 0.0
    previous_value = dimensions._safe_float(previous, None)
    if previous_value is None:
        return round(sample_value, 4)
    result = (1.0 - alpha_value) * previous_value + alpha_value * sample_value
    return round(result, 4) if math.isfinite(result) else 0.0


def compute_frame_points(
    grade_dict: typing.Mapping[str, typing.Any],
    telescope_stats: typing.Mapping[str, typing.Any] | None = None,
    *,
    base_points: float | None = None,
    campaign_multiplier: float = 1.0,
) -> schema.PointsResult:
    """
    Compute cumulative points for one graded frame upload.

    Args:
        grade_dict: Grade payload or subset with ``dimensions`` and optional ``sp_exptime``.
        telescope_stats: Prior telescope totals; uses ``total_exposure_seconds`` for tenure.
        base_points: Override default :data:`grading.constants.BASE_POINTS`.

    Returns:
        Breakdown dict with ``points_earned`` (float, 2 dp).
    """
    dims = grade_dict.get("dimensions") or {}
    image_quality = dimensions.dim_score(dims, "image_quality")
    timeliness = dimensions.dim_score(dims, "timeliness")

    quality_multiplier = 0.5 + 0.5 * image_quality
    exptime = dimensions._safe_float(grade_dict.get("sp_exptime")) or 0.0
    if exptime > 0:
        exptime_factor = min(1.0, exptime / constants.EXPTIME_CAP_SECONDS)
    else:
        exptime_factor = 0.25
    timeliness_factor = 0.5 + 0.5 * timeliness

    stats = telescope_stats or {}
    total_exposure_seconds = dimensions._safe_float(stats.get("total_exposure_seconds")) or 0.0
    total_hours = max(0.0, total_exposure_seconds) / 3600.0
    tenure_multiplier = 1.0 + math.log1p(total_hours) * constants.TENURE_LOG_SCALE

    base_points_value = dimensions._safe_float(base_points, None)
    bp = constants.BASE_POINTS if base_points_value is None else base_points_value
    campaign_multiplier_value = dimensions._safe_float(campaign_multiplier, None)
    mult = (
        campaign_multiplier_value
        if campaign_multiplier_value is not None and campaign_multiplier_value > 0
        else 1.0
    )
    points = bp * quality_multiplier * exptime_factor * timeliness_factor * tenure_multiplier * mult
    if not math.isfinite(points):
        points = 0.0

    return {
        "base_points": bp,
        "quality_multiplier": round(quality_multiplier, 4),
        "exptime_factor": round(exptime_factor, 4),
        "timeliness_factor": round(timeliness_factor, 4),
        "tenure_multiplier": round(tenure_multiplier, 4),
        "campaign_multiplier": round(mult, 4),
        "sp_exptime": exptime,
        "points_earned": round(points, 2),
    }


def build_reputation_partial(
    existing: typing.Mapping[str, typing.Any] | None,
    *,
    headline: int,
    dimensions_map: typing.Mapping[str, typing.Any],
    points_earned: float,
    sp_exptime: float,
) -> schema.ReputationPartial:
    """
    Merge grade into telescope ``reputation_stats`` partial update.

    Updates cumulative totals and EMA-derived ``reliability_score``.
    """
    stats = dict(existing or {})
    prev_reliability = stats.get("reliability_score")
    prev_reliability = dimensions._safe_float(prev_reliability, None)

    prev_headline = stats.get("task_quality_score")
    prev_headline = dimensions._safe_float(prev_headline, None)

    image_quality = dimensions.dim_score(dimensions_map, "image_quality")
    total_points = (dimensions._safe_float(stats.get("total_points")) or 0.0) + (
        dimensions._safe_float(points_earned) or 0.0
    )
    frame_count_value = dimensions._safe_float(stats.get("frame_count"), None)
    frame_count = max(0, int(frame_count_value or 0)) + 1
    exposure_value = dimensions._safe_float(sp_exptime) or 0.0
    total_exposure = (dimensions._safe_float(stats.get("total_exposure_seconds")) or 0.0) + max(
        0.0, exposure_value
    )

    if not math.isfinite(total_points):
        total_points = 0.0
    if not math.isfinite(total_exposure):
        total_exposure = 0.0

    points_per_hour = None
    if total_exposure > 0:
        rate = total_points / (total_exposure / 3600.0)
        points_per_hour = round(rate, 2) if math.isfinite(rate) else 0.0

    headline_value = dimensions._safe_float(headline, 0.0) or 0.0
    headline_value = dimensions.clamp(headline_value / 100.0)

    return {
        "total_points": round(total_points, 2),
        "frame_count": frame_count,
        "total_exposure_seconds": round(total_exposure, 1),
        "points_per_hour": points_per_hour,
        "reliability_score": ema_update(
            prev_reliability, image_quality, constants.RELIABILITY_EMA_ALPHA
        ),
        "task_quality_score": ema_update(
            prev_headline, headline_value, constants.HEADLINE_EMA_ALPHA
        ),
        "last_ingested_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        "source": "grade_ingest",
    }
