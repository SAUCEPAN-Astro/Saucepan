"""
Emulator frame classification — provenance flags for SP_EMULATOR FITS headers.

Emulator frames run the full grading/ingest/stack pipeline; they are tagged so
catalog and science products can distinguish synthetic from real data.

Assignment isolation (allow_emulator / is_emulator) remains upstream in task-server.
See tools/emulator/EMULATOR_ISOLATION.md safeguard #7.
"""

from __future__ import annotations

import typing
from dataclasses import dataclass


@dataclass(frozen=True)
class FrameClassification:
    """Provenance tags applied to grade payloads and downstream catalog rows."""

    sp_emulator: bool
    data_tier: str  # "science" | "emulator"
    science_eligible: bool


def _truthy(value: typing.Any) -> bool:
    if value is True:
        return True
    if value is False or value is None:
        return False
    if isinstance(value, (int, float)):
        return value != 0
    return str(value).strip().lower() in {"1", "true", "t", "yes"}


def is_emulator_header(headers: typing.Mapping[str, typing.Any]) -> bool:
    """True when FITS carries SP_EMULATOR=1 (set by network emulator)."""
    return _truthy(headers.get("sp_emulator"))


def is_sandbox_task(task_context: typing.Mapping[str, typing.Any]) -> bool:
    """True when upload/task metadata marks an explicit sandbox (allow_emulator)."""
    return _truthy(task_context.get("allow_emulator"))


def classify_frame(
    headers: typing.Mapping[str, typing.Any],
    task_context: typing.Mapping[str, typing.Any] | None = None,
) -> FrameClassification:
    """
    Classify frame provenance for grade/stack metadata.

    Emulator frames always get data_tier=emulator and science_eligible=False.
    Real frames get data_tier=science and science_eligible=True.
    """
    _ = task_context  # reserved for future sandbox-vs-production sub-tiers
    if is_emulator_header(headers):
        return FrameClassification(
            sp_emulator=True,
            data_tier="emulator",
            science_eligible=False,
        )
    return FrameClassification(
        sp_emulator=False,
        data_tier="science",
        science_eligible=True,
    )


def stack_cohort_error(
    header_sets: typing.Sequence[typing.Mapping[str, typing.Any]],
) -> str | None:
    """
    Return an error message when a stack mixes emulator and science frames.

    Emulator-only and science-only stacks are both allowed (full pipeline).
    """
    if not header_sets:
        return None
    flags = {is_emulator_header(h) for h in header_sets}
    if len(flags) > 1:
        return "Cannot mix emulator (SP_EMULATOR) and science frames in one stack"
    return None
