"""
Pure scoring functions for per-frame grade dimensions (no I/O).
"""

from __future__ import annotations

import math
import typing
from datetime import datetime, timezone

from grading import constants


def _safe_float(value: typing.Any, default: float | None = 0.0) -> float | None:
    """Return a finite float, or ``default`` for malformed numeric input."""
    try:
        number = float(value)
    except (TypeError, ValueError):
        return default
    return number if math.isfinite(number) else default


def clamp(value: float, lo: float = 0.0, hi: float = 1.0) -> float:
    if not math.isfinite(value):
        return lo
    return max(lo, min(hi, value))


def parse_iso8601(value: str | None) -> datetime | None:
    if not value:
        return None
    text = value.strip()
    if text.endswith("Z"):
        text = text[:-1] + "+00:00"
    try:
        dt = datetime.fromisoformat(text)
    except ValueError:
        return None
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=timezone.utc)
    return dt.astimezone(timezone.utc)


def dim_score(dimensions: typing.Mapping[str, typing.Any], key: str) -> float:
    dim = dimensions.get(key) or {}
    return clamp(_safe_float(dim.get("score")) or 0.0)


def score_image_quality(
    metrics: typing.Mapping[str, typing.Any],
    headers: typing.Mapping[str, typing.Any],
    predicted_psf_arcsec: float | None = None,
) -> dict[str, typing.Any]:
    """SNR, noise, saturation proxy; FWHM from header when present."""
    snr = _safe_float(metrics.get("snr")) or 0.0
    noise_adu = _safe_float(metrics.get("noise_adu")) or 0.0
    shape = metrics.get("shape") or [1, 1]
    total_pixels = max(1, int(shape[0]) * int(shape[1]))
    saturated_pixels = _safe_float(metrics.get("saturated_pixels", 0)) or 0.0
    sat_frac = max(0.0, saturated_pixels) / total_pixels

    snr_score = clamp(snr / constants.SNR_FULL_CREDIT)
    sat_score = clamp(1.0 - (sat_frac / constants.SATURATION_PENALTY_FRACTION))

    measured_fwhm = headers.get("sp_fwhm")
    measured_fwhm_value = _safe_float(measured_fwhm, None)
    predicted_psf = _safe_float(predicted_psf_arcsec, None)
    fwhm_source = "header" if measured_fwhm is not None else "missing"
    if (
        measured_fwhm_value is not None
        and measured_fwhm_value > 0
        and predicted_psf is not None
        and predicted_psf > 0
    ):
        fwhm_score = clamp(predicted_psf / measured_fwhm_value)
    elif _safe_float(headers.get("sp_qual"), None) is not None:
        fwhm_score = clamp(_safe_float(headers["sp_qual"], 0.0) or 0.0)
        fwhm_source = "sp_qual_proxy"
    else:
        fwhm_score = constants.NEUTRAL_FWHM_SCORE
        fwhm_source = "neutral"

    w = constants.IMAGE_QUALITY_WEIGHTS
    score = clamp(w["snr"] * snr_score + w["saturation"] * sat_score + w["fwhm"] * fwhm_score)

    return {
        "score": round(score, 4),
        "snr": snr,
        "noise_adu": noise_adu,
        "saturation_fraction": round(sat_frac, 6),
        "fwhm_arcsec": measured_fwhm_value,
        "fwhm_source": fwhm_source,
        "star_pixels": int(_safe_float(metrics.get("star_pixels", 0)) or 0),
    }


def score_task_fidelity(
    headers: typing.Mapping[str, typing.Any],
    task_context: typing.Mapping[str, typing.Any],
) -> dict[str, typing.Any]:
    """Exptime vs requested integration; filter match; calibration bonus."""
    exptime = _safe_float(headers.get("sp_exptime"), None)
    requested = _safe_float(task_context.get("integration_time_requested"), None)
    if exptime is not None and requested and requested > 0:
        exptime_ratio = clamp(exptime / requested)
    else:
        exptime_ratio = None

    filter_requested = (task_context.get("filter_requested") or "").strip().upper()
    filter_actual = (headers.get("sp_filter") or "").strip().upper()
    if filter_requested and filter_actual:
        requested_filters = {
            value.strip() for value in filter_requested.split(",") if value.strip()
        }
        filter_match = filter_actual in requested_filters
        filter_score = 1.0 if filter_match else 0.0
    else:
        filter_match = None
        filter_score = constants.FILTER_ABSENT_SCORE

    calstat = (headers.get("sp_calstat") or "NONE").upper()
    cal_bonus = constants.CALIBRATION_BONUS if calstat in constants.CALIBRATED_STATUSES else 0.0

    if exptime_ratio is not None:
        score = clamp(0.7 * exptime_ratio + 0.3 * filter_score + cal_bonus)
    else:
        score = clamp(0.5 * filter_score + 0.5 + cal_bonus * 0.5)

    return {
        "score": round(score, 4),
        "exptime_ratio": round(exptime_ratio, 4) if exptime_ratio is not None else None,
        "filter_match": filter_match,
        "filter_requested": filter_requested or None,
        "filter_actual": filter_actual or None,
        "calstat": calstat,
    }


def score_timeliness(task_context: typing.Mapping[str, typing.Any]) -> dict[str, typing.Any]:
    """
    Timeliness from message timestamps only — no FITS read.

    capture_latency: assignment_sent_at → upload_completed_at (proxy for end-to-end).
    upload_duration: optional separate upload_started_at → upload_completed_at.
    """
    assignment_at = parse_iso8601(task_context.get("assignment_sent_at"))
    upload_at = parse_iso8601(
        task_context.get("upload_completed_at") or task_context.get("upload_time")
    )
    upload_start = parse_iso8601(task_context.get("upload_started_at"))

    capture_latency_sec = None
    upload_duration_sec = None

    if assignment_at and upload_at:
        capture_latency_sec = max(0.0, (upload_at - assignment_at).total_seconds())
    if upload_start and upload_at:
        upload_duration_sec = max(0.0, (upload_at - upload_start).total_seconds())

    if capture_latency_sec is not None:
        span = constants.CAPTURE_LATENCY_ZERO_SEC - constants.CAPTURE_LATENCY_FULL_SEC
        capture_score = clamp(
            1.0 - (capture_latency_sec - constants.CAPTURE_LATENCY_FULL_SEC) / span
        )
    else:
        capture_score = constants.MISSING_TIMELINESS_SCORE

    if upload_duration_sec is not None:
        span = constants.UPLOAD_DURATION_ZERO_SEC - constants.UPLOAD_DURATION_FULL_SEC
        upload_score = clamp(
            1.0 - (upload_duration_sec - constants.UPLOAD_DURATION_FULL_SEC) / span
        )
    else:
        upload_score = capture_score

    score = clamp(
        constants.TIMELINESS_CAPTURE_WEIGHT * capture_score
        + constants.TIMELINESS_UPLOAD_WEIGHT * upload_score
    )

    return {
        "score": round(score, 4),
        "capture_latency_sec": (
            round(capture_latency_sec, 1) if capture_latency_sec is not None else None
        ),
        "upload_duration_sec": (
            round(upload_duration_sec, 1) if upload_duration_sec is not None else None
        ),
    }


def _plate_solve_ok(headers: typing.Mapping[str, typing.Any]) -> bool:
    ctype1 = str(headers.get("ctype1") or "").strip().upper()
    ctype2 = str(headers.get("ctype2") or "").strip().upper()
    return (
        ctype1.startswith("RA---")
        and ctype2.startswith("DEC--")
        and _safe_float(headers.get("crval1"), None) is not None
        and _safe_float(headers.get("crpix1"), None) is not None
        and _safe_float(headers.get("crval2"), None) is not None
        and _safe_float(headers.get("crpix2"), None) is not None
    )


def score_stack_compatibility(
    headers: typing.Mapping[str, typing.Any],
    quality_metrics: typing.Mapping[str, typing.Any],
    task_context: typing.Mapping[str, typing.Any],
) -> dict[str, typing.Any]:
    """Continuous 0–1 suitability for heterogeneous stacking."""
    measured_fwhm = headers.get("sp_fwhm")
    measured_fwhm_value = _safe_float(measured_fwhm, None)
    max_psf = task_context.get("max_psf_fwhm") or task_context.get("predicted_psf_arcsec")
    max_psf_value = _safe_float(max_psf, None)
    if (
        measured_fwhm_value is not None
        and measured_fwhm_value > 0
        and max_psf_value is not None
        and max_psf_value > 0
    ):
        fwhm_score = clamp(max_psf_value / measured_fwhm_value)
        fwhm_source = "computed"
    else:
        fwhm_score = constants.STACK_COMPAT_NEUTRAL
        fwhm_source = "missing"

    frame_ps = _safe_float(headers.get("sp_pixscale"), None)
    target_ps = task_context.get("contrib_pixscale") or task_context.get("max_resolution")
    target_ps_value = _safe_float(target_ps, None)
    if (
        frame_ps is not None
        and frame_ps > 0
        and target_ps_value is not None
        and target_ps_value > 0
    ):
        ratio = min(frame_ps, target_ps_value) / max(frame_ps, target_ps_value)
        pixscale_score = clamp(ratio)
        pixscale_source = "computed"
    else:
        pixscale_score = constants.STACK_COMPAT_NEUTRAL
        pixscale_source = "missing"

    snr = _safe_float(quality_metrics.get("snr"), None)
    noise_adu = _safe_float(quality_metrics.get("noise_adu"), None)
    if snr is not None and snr > 0:
        noise_score = clamp(snr / constants.SNR_FULL_CREDIT)
        noise_source = "snr_proxy"
    elif noise_adu is not None:
        noise_score = clamp(1.0 - noise_adu / constants.STACK_NOISE_FULL_ADU)
        noise_source = "noise_adu"
    else:
        noise_score = constants.STACK_COMPAT_NEUTRAL
        noise_source = "missing"

    w = constants.STACK_COMPAT_WEIGHTS
    score = clamp(
        w["fwhm"] * fwhm_score + w["pixscale"] * pixscale_score + w["noise"] * noise_score
    )

    return {
        "score": round(score, 4),
        "fwhm_score": round(fwhm_score, 4),
        "fwhm_arcsec": measured_fwhm_value,
        "fwhm_limit_arcsec": max_psf_value if max_psf_value and max_psf_value > 0 else None,
        "fwhm_source": fwhm_source,
        "pixscale_score": round(pixscale_score, 4),
        "pixscale_arcsec": frame_ps,
        "target_pixscale_arcsec": (
            target_ps_value if target_ps_value is not None and target_ps_value > 0 else None
        ),
        "pixscale_source": pixscale_source,
        "noise_score": round(noise_score, 4),
        "noise_source": noise_source,
    }


def score_reliability(
    headers: typing.Mapping[str, typing.Any],
    quality_metrics: typing.Mapping[str, typing.Any],
    task_context: typing.Mapping[str, typing.Any],
) -> dict[str, typing.Any]:
    """Per-frame reliability heuristic for alpha (calibration, plate solve, timeliness)."""
    del quality_metrics  # reserved for future signal-quality hooks

    calstat = (headers.get("sp_calstat") or "NONE").upper()
    cal_score = (
        1.0
        if calstat in constants.CALIBRATED_STATUSES
        else constants.RELIABILITY_UNCALIBRATED_SCORE
    )

    plate_ok = _plate_solve_ok(headers)
    plate_score = 1.0 if plate_ok else constants.RELIABILITY_NO_PLATE_SCORE

    tl = score_timeliness(task_context)
    tl_score = float(tl.get("score") or constants.MISSING_TIMELINESS_SCORE)

    w = constants.RELIABILITY_WEIGHTS
    score = clamp(
        w["calibration"] * cal_score + w["plate_solve"] * plate_score + w["timeliness"] * tl_score
    )

    return {
        "score": round(score, 4),
        "calstat": calstat,
        "cal_score": round(cal_score, 4),
        "plate_solve_ok": int(plate_ok),
        "plate_score": round(plate_score, 4),
        "timeliness_score": round(tl_score, 4),
    }


def headline_score(dimensions: typing.Mapping[str, typing.Any]) -> int:
    """Weighted 0–100 headline from cheap dimension subscores."""
    total = 0.0
    for key, weight in constants.CHEAP_DIMENSION_WEIGHTS.items():
        total += weight * dim_score(dimensions, key)
    return int(round(100 * clamp(total)))
