"""
JSON-serializable types for grade payloads and ingest contracts.
"""

from __future__ import annotations

import typing


class DimensionDetail(typing.TypedDict, total=False):
    score: float
    snr: float
    noise_adu: float
    saturation_fraction: float
    fwhm_arcsec: float | None
    fwhm_source: str
    star_pixels: int
    exptime_ratio: float | None
    filter_match: bool | None
    filter_requested: str | None
    filter_actual: str | None
    calstat: str
    capture_latency_sec: float | None
    upload_duration_sec: float | None


class GradeDimensions(typing.TypedDict, total=False):
    image_quality: DimensionDetail
    task_fidelity: DimensionDetail
    timeliness: DimensionDetail
    reliability: DimensionDetail | None
    scientific_value: DimensionDetail | None
    stack_compatibility: DimensionDetail | None


class QualityMetrics(typing.TypedDict, total=False):
    snr: float | None
    noise_adu: float | None
    star_pixels: int | None
    saturated_pixels: int | None


class GradePayload(typing.TypedDict, total=False):
    upload_id: str | None
    task_id: int | None
    telescope_id: str | None
    integration_time_requested: float | None
    sp_exptime: float | None
    grader_version: str
    idempotency_key: str | None
    dimensions: GradeDimensions
    headline: int
    quality_metrics: QualityMetrics
    graded_at: str
    sp_emulator: bool
    data_tier: str
    science_eligible: bool
    stack_eligible: bool
    sp_ra: float | None
    sp_dec: float | None
    sp_dateobs: str | None
    sp_filter: str | None
    sp_fwhm: float | None
    sp_calstat: str | None
    sp_snr: float | None
    campaign_id: str | None
    frame_id: str | None
    object_key: str | None


class TelescopeStats(typing.TypedDict, total=False):
    total_exposure_seconds: float
    total_points: float
    frame_count: int
    reliability_score: float | None
    task_quality_score: float | None
    points_per_hour: float | None


class PointsResult(typing.TypedDict):
    base_points: float
    quality_multiplier: float
    exptime_factor: float
    timeliness_factor: float
    tenure_multiplier: float
    campaign_multiplier: float
    sp_exptime: float
    points_earned: float


class ReputationPartial(typing.TypedDict, total=False):
    total_points: float
    frame_count: int
    total_exposure_seconds: float
    points_per_hour: float | None
    reliability_score: float
    task_quality_score: float
    last_ingested_at: str
    source: str


class IngestRequest(typing.TypedDict, total=False):
    """Body for POST /api/v1/grades/ingest."""

    upload_id: str
    task_id: int | None
    telescope_id: str
    idempotency_key: str
    headline: int
    sp_exptime: float | None
    integration_time_requested: float | None
    grader_version: str
    dimensions: GradeDimensions
    quality_metrics: QualityMetrics
