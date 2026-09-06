"""Edge cases for grading.stack_filter (pure filtering helper)."""

from __future__ import annotations

from grading.stack_filter import filter_stack_eligible_grades, is_stack_eligible


def test_filter_stack_eligible_grades_empty_list():
    assert filter_stack_eligible_grades([]) == []


def test_filter_stack_eligible_grades_keeps_only_flagged():
    grades = [
        {"id": 1, "stack_eligible": True},
        {"id": 2, "stack_eligible": False},
        {"id": 3, "stack_eligible": True},
        {"id": 4},  # missing key -> falsy
    ]
    kept = filter_stack_eligible_grades(grades)
    assert [g["id"] for g in kept] == [1, 3]


def test_is_stack_eligible_at_exact_threshold():
    from grading import constants

    dims = {"image_quality": {"score": constants.STACK_ELIGIBLE_MIN_QUALITY}}
    assert is_stack_eligible(dims) is True


def test_is_stack_eligible_missing_dimension_false():
    assert is_stack_eligible({}) is False
