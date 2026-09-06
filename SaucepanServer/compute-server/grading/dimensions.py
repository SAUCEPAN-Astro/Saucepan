"""
Pure scoring functions for per-frame grade dimensions (no I/O).
"""

from __future__ import annotations

import typing
from datetime import datetime, timezone

from grading import constants


def clamp(value: float, lo: float = 0.0, hi: float = 1.0) -> float:
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
    try:
        return clamp(float(dim.get("score") or 0.0))
    except (TypeError, ValueError):
        return 0.0


def score_image_quality(
    metrics: typing.Mapping[str, typing.Any],
    headers: typing.Mapping[str, typing.Any],
    predicted_psf_arcsec: float | None = None,
) -> dict[str, typing.Any]:
    """SNR, noise, saturation proxy; FWHM from header when present."""
    snr = float(metrics.get("snr") or 0.0)
    noise_adu = float(metrics.get("noise_adu") or 0.0)
    shape = metrics.get("shape") or [1, 1]
    total_pixels = max(1, int(shape[0]) * int(shape[1]))
    sat_frac = float(metrics.get("saturated_pixels", 0)) / total_pixels

    snr_score = clamp(snr / constants.SNR_FULL_CREDIT)
    sat_score = clamp(1.0 - (sat_frac / constants.SATURATION_PENALTY_FRACTION))

    measured_fwhm = headers.get("sp_fwhm")
    fwhm_source = "header" if measured_fwhm is not None else "missing"
    if measured_fwhm is not None and predicted_psf_arcsec and predicted_psf_arcsec > 0:
        fwhm_score = clamp(predicted_psf_arcsec / measured_fwhm)
    elif headers.get("sp_qual") is not None:
        fwhm_score = clamp(float(headers["sp_qual"]))
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
        "fwhm_arcsec": measured_fwhm,
        "fwhm_source": fwhm_source,
        "star_pixels": int(metrics.get("star_pixels", 0)),
    }


def score_task_fidelity(
    headers: typing.Mapping[str, typing.Any],
    task_context: typing.Mapping[str, typing.Any],
) -> dict[str, typing.Any]:
    """Exptime vs requested integration; filter match; calibration bonus."""
    exptime = headers.get("sp_exptime")
    requested = task_context.get("integration_time_requested")
    if exptime is not None and requested and requested > 0:
        exptime_ratio = clamp(float(exptime) / float(requested))
    else:
        exptime_ratio = None

    filter_requested = (task_context.get("filter_requested") or "").strip().upper()
    filter_actual = (headers.get("sp_filter") or "").strip().upper()
    if filter_requested and filter_actual:
        filter_match = filter_actual == filter_requested or filter_actual in filter_requested
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
    return all(headers.get(k) is not None for k in ("ctype1", "crval1", "crpix1"))


def score_stack_compatibility(
    headers: typing.Mapping[str, typing.Any],
    quality_metrics: typing.Mapping[str, typing.Any],
    task_context: typing.Mapping[str, typing.Any],
) -> dict[str, typing.Any]:
    """Continuous 0–1 suitability for heterogeneous stacking."""
    measured_fwhm = headers.get("sp_fwhm")
    max_psf = task_context.get("max_psf_fwhm") or task_context.get("predicted_psf_arcsec")
    if measured_fwhm is not None and max_psf and float(max_psf) > 0:
        fwhm_score = clamp(float(max_psf) / float(measured_fwhm))
        fwhm_source = "computed"
    else:
        fwhm_score = constants.STACK_COMPAT_NEUTRAL
        fwhm_source = "missing"

    frame_ps = headers.get("sp_pixscale")
    target_ps = task_context.get("contrib_pixscale") or task_context.get("max_resolution")
    if frame_ps is not None and target_ps and float(target_ps) > 0:
        ratio = min(float(frame_ps), float(target_ps)) / max(float(frame_ps), float(target_ps))
        pixscale_score = clamp(ratio)
        pixscale_source = "computed"
    else:
        pixscale_score = constants.STACK_COMPAT_NEUTRAL
        pixscale_source = "missing"

    snr = quality_metrics.get("snr")
    noise_adu = quality_metrics.get("noise_adu")
    if snr is not None and float(snr) > 0:
        noise_score = clamp(float(snr) / constants.SNR_FULL_CREDIT)
        noise_source = "snr_proxy"
    elif noise_adu is not None:
        noise_score = clamp(1.0 - float(noise_adu) / constants.STACK_NOISE_FULL_ADU)
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
        "fwhm_arcsec": measured_fwhm,
        "fwhm_limit_arcsec": float(max_psf) if max_psf else None,
        "fwhm_source": fwhm_source,
        "pixscale_score": round(pixscale_score, 4),
        "pixscale_arcsec": frame_ps,
        "target_pixscale_arcsec": float(target_ps) if target_ps else None,
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
