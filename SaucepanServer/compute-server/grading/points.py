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
    if previous is None:
        return round(sample, 4)
    return round((1.0 - alpha) * previous + alpha * sample, 4)


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
    exptime = float(grade_dict.get("sp_exptime") or 0.0)
    if exptime > 0:
        exptime_factor = min(1.0, exptime / constants.EXPTIME_CAP_SECONDS)
    else:
        exptime_factor = 0.25
    timeliness_factor = 0.5 + 0.5 * timeliness

    stats = telescope_stats or {}
    total_exposure_seconds = float(stats.get("total_exposure_seconds") or 0.0)
    total_hours = max(0.0, total_exposure_seconds) / 3600.0
    tenure_multiplier = 1.0 + math.log1p(total_hours) * constants.TENURE_LOG_SCALE

    bp = constants.BASE_POINTS if base_points is None else base_points
    mult = campaign_multiplier if campaign_multiplier > 0 else 1.0
    points = bp * quality_multiplier * exptime_factor * timeliness_factor * tenure_multiplier * mult

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
    if isinstance(prev_reliability, (int, float)):
        prev_reliability = float(prev_reliability)
    else:
        prev_reliability = None

    prev_headline = stats.get("task_quality_score")
    if isinstance(prev_headline, (int, float)):
        prev_headline = float(prev_headline)
    else:
        prev_headline = None

    image_quality = dimensions.dim_score(dimensions_map, "image_quality")
    total_points = float(stats.get("total_points") or 0.0) + points_earned
    frame_count = int(stats.get("frame_count") or 0) + 1
    total_exposure = float(stats.get("total_exposure_seconds") or 0.0) + max(0.0, sp_exptime)

    points_per_hour = None
    if total_exposure > 0:
        points_per_hour = round(total_points / (total_exposure / 3600.0), 2)

    return {
        "total_points": round(total_points, 2),
        "frame_count": frame_count,
        "total_exposure_seconds": round(total_exposure, 1),
        "points_per_hour": points_per_hour,
        "reliability_score": ema_update(
            prev_reliability, image_quality, constants.RELIABILITY_EMA_ALPHA
        ),
        "task_quality_score": ema_update(
            prev_headline, headline / 100.0, constants.HEADLINE_EMA_ALPHA
        ),
        "last_ingested_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        "source": "grade_ingest",
    }
