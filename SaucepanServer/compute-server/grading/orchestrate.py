"""
Assemble a full grade payload from quality metrics + headers (no FITS I/O).
"""

from __future__ import annotations

import typing
from datetime import datetime, timezone

from grading import constants, dimensions
from grading.emulator_policy import FrameClassification, classify_frame
from grading.stack_filter import is_stack_eligible


def build_grade_payload(
    task_context: typing.Mapping[str, typing.Any],
    *,
    quality_metrics: typing.Mapping[str, typing.Any],
    headers: typing.Mapping[str, typing.Any],
    grader_version: str | None = None,
    classification: FrameClassification | None = None,
) -> dict[str, typing.Any]:
    """
    Build grade JSON from pre-computed quality metrics and SP_* headers.

    Used by Lambda and datalake sync path after ``quality.assess_fits``.
    """
    upload_id = task_context.get("upload_id")
    idempotency_key = task_context.get("idempotency_key") or (
        f"{upload_id}:grade" if upload_id else None
    )
    predicted_psf = task_context.get("predicted_psf_arcsec")
    provenance = classification or classify_frame(headers, task_context)

    dims: dict[str, typing.Any] = {
        "image_quality": dimensions.score_image_quality(quality_metrics, headers, predicted_psf),
        "task_fidelity": dimensions.score_task_fidelity(headers, task_context),
        "timeliness": dimensions.score_timeliness(task_context),
        "reliability": dimensions.score_reliability(headers, quality_metrics, task_context),
        "scientific_value": None,
        "stack_compatibility": dimensions.score_stack_compatibility(
            headers, quality_metrics, task_context
        ),
    }

    stack_eligible = is_stack_eligible(dims)

    return {
        "upload_id": upload_id,
        "task_id": task_context.get("task_id"),
        "telescope_id": task_context.get("telescope_id"),
        "integration_time_requested": task_context.get("integration_time_requested"),
        "sp_exptime": headers.get("sp_exptime"),
        "sp_emulator": provenance.sp_emulator,
        "data_tier": provenance.data_tier,
        "science_eligible": provenance.science_eligible,
        "grader_version": grader_version or constants.GRADER_VERSION,
        "idempotency_key": idempotency_key,
        "dimensions": dims,
        "headline": dimensions.headline_score(dims),
        "stack_eligible": stack_eligible,
        "quality_metrics": {
            "snr": quality_metrics.get("snr"),
            "noise_adu": quality_metrics.get("noise_adu"),
            "star_pixels": quality_metrics.get("star_pixels"),
            "saturated_pixels": quality_metrics.get("saturated_pixels"),
        },
        "graded_at": datetime.now(timezone.utc).isoformat(),
        # L1 catalog fields — mirrored into frame_catalog on ingest (#33)
        "sp_ra": headers.get("sp_ra"),
        "sp_dec": headers.get("sp_dec"),
        "sp_dateobs": headers.get("sp_dateobs"),
        "sp_filter": headers.get("sp_filter"),
        "sp_fwhm": headers.get("sp_fwhm"),
        "sp_calstat": headers.get("sp_calstat"),
        "sp_snr": headers.get("sp_snr"),
        "campaign_id": task_context.get("campaign_id"),
        "frame_id": task_context.get("frame_id"),
        "object_key": task_context.get("s3_key") or task_context.get("object_key"),
    }
