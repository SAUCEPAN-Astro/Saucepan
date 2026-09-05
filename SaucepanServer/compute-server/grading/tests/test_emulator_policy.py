"""Tests for SP_EMULATOR provenance classification."""

from grading.emulator_policy import (
    classify_frame,
    is_emulator_header,
    is_sandbox_task,
    stack_cohort_error,
)


def test_real_frame_classified_science():
    cls = classify_frame({}, {"allow_emulator": False})
    assert not cls.sp_emulator
    assert cls.data_tier == "science"
    assert cls.science_eligible


def test_emulator_frame_classified():
    cls = classify_frame({"sp_emulator": True}, {"allow_emulator": True})
    assert cls.sp_emulator
    assert cls.data_tier == "emulator"
    assert not cls.science_eligible


def test_emulator_on_production_task_still_classified_not_rejected():
    """Full pipeline runs; provenance flags distinguish synthetic data."""
    cls = classify_frame({"sp_emulator": 1}, {"task_id": 42, "allow_emulator": False})
    assert cls.sp_emulator
    assert cls.data_tier == "emulator"
    assert not cls.science_eligible


def test_stack_cohort_mixed_error():
    err = stack_cohort_error([{"sp_emulator": 0}, {"sp_emulator": 1}])
    assert err is not None
    assert "mix" in err.lower()


def test_stack_cohort_homogeneous_ok():
    assert stack_cohort_error([{"sp_emulator": 1}, {"sp_emulator": True}]) is None
    assert stack_cohort_error([{}, {}]) is None


def test_truthy_helpers():
    assert is_emulator_header({"sp_emulator": "yes"})
    assert is_sandbox_task({"allow_emulator": 1})
    assert not is_emulator_header({"sp_emulator": 0})
    assert not is_sandbox_task({"allow_emulator": "false"})


def test_stack_cohort_error_empty_header_sets_returns_none():
    assert stack_cohort_error([]) is None


def test_stack_cohort_error_single_frame_no_mix_possible():
    assert stack_cohort_error([{"sp_emulator": True}]) is None


def test_classify_frame_no_task_context_defaults_none():
    cls = classify_frame({})
    assert cls.data_tier == "science"


def test_truthy_edge_values():
    from grading.emulator_policy import _truthy

    assert _truthy(1) is True
    assert _truthy(0) is False
    assert _truthy(None) is False
    assert _truthy(False) is False
    assert _truthy(2.5) is True
    assert _truthy("T") is True
    assert _truthy("nope") is False
