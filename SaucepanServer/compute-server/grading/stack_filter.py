"""
Stack eligibility pre-filter (pure logic).
"""

from __future__ import annotations

import typing

from grading import constants, dimensions


def is_stack_eligible(subscores: typing.Mapping[str, typing.Any]) -> bool:
    """True when frame image_quality subscore meets the stack threshold."""
    return dimensions.dim_score(subscores, "image_quality") >= constants.STACK_ELIGIBLE_MIN_QUALITY


def filter_stack_eligible_grades(
    grades: typing.Iterable[typing.Mapping[str, typing.Any]],
) -> list[typing.Mapping[str, typing.Any]]:
    """Helper for stacking jobs: keep only frames marked stack-eligible."""
    return [g for g in grades if g.get("stack_eligible")]
