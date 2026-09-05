"""
saucepan-grading — shared per-frame grading, points, and stack eligibility.

Import examples::

    from grading.constants import STACK_ELIGIBLE_MIN_QUALITY, BASE_POINTS
    from grading.dimensions import score_image_quality, headline_score
    from grading.points import compute_frame_points, build_reputation_partial
    from grading.stack_filter import is_stack_eligible
    from grading.orchestrate import build_grade_payload
"""

from grading.constants import (
    BASE_POINTS,
    CHEAP_DIMENSION_WEIGHTS,
    EXPTIME_CAP_SECONDS,
    GRADER_VERSION,
    HEADLINE_EMA_ALPHA,
    RELIABILITY_EMA_ALPHA,
    STACK_ELIGIBLE_MIN_QUALITY,
    TENURE_LOG_SCALE,
)
from grading.dimensions import (
    dim_score,
    headline_score,
    score_image_quality,
    score_task_fidelity,
    score_timeliness,
)
from grading.orchestrate import build_grade_payload
from grading.points import build_reputation_partial, compute_frame_points, ema_update
from grading.stack_filter import filter_stack_eligible_grades, is_stack_eligible

__all__ = [
    "BASE_POINTS",
    "CHEAP_DIMENSION_WEIGHTS",
    "EXPTIME_CAP_SECONDS",
    "GRADER_VERSION",
    "HEADLINE_EMA_ALPHA",
    "RELIABILITY_EMA_ALPHA",
    "STACK_ELIGIBLE_MIN_QUALITY",
    "TENURE_LOG_SCALE",
    "build_grade_payload",
    "build_reputation_partial",
    "compute_frame_points",
    "dim_score",
    "ema_update",
    "filter_stack_eligible_grades",
    "headline_score",
    "is_stack_eligible",
    "score_image_quality",
    "score_task_fidelity",
    "score_timeliness",
]
