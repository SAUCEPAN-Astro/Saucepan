"""Producer: grade payload metrics (calls grading SSOT)."""

from __future__ import annotations

import typing

from metrics.observation import EntityContext


def produce(ctx: EntityContext) -> dict[str, typing.Any]:
    grade = ctx.get("_grade_result")
    if isinstance(grade, dict):
        return _from_grade(grade, ctx=ctx)
    path = ctx.get("staged_path")
    if not path:
        return {}
    grade = _grade_fits(path, ctx)
    if not grade:
        return {}
    return _from_grade(grade, ctx=ctx)


def _from_grade(
    grade: dict[str, typing.Any],
    *,
    ctx: EntityContext | None = None,
) -> dict[str, typing.Any]:
    dims = grade.get("dimensions") or {}
    iq = dims.get("image_quality") or {}
    tf = dims.get("task_fidelity") or {}
    tl = dims.get("timeliness") or {}
    rel = dims.get("reliability") or {}
    sc = dims.get("stack_compatibility") or {}
    pb = grade.get("points_breakdown") or {}

    out: dict[str, typing.Any] = {
        "grade.headline": grade.get("headline"),
        "grade.version": grade.get("grader_version"),
        "grade.stack_eligible": grade.get("stack_eligible"),
        "grade.sp_emulator": grade.get("sp_emulator"),
        "grade.data_tier": grade.get("data_tier"),
        "grade.science_eligible": grade.get("science_eligible"),
        "grade.points_earned": pb.get("points_earned") or grade.get("points_earned"),
        "grade.image_quality_score": iq.get("score"),
        "grade.task_fidelity_score": tf.get("score"),
        "grade.timeliness_score": tl.get("score"),
        "grade.reliability_score": rel.get("score"),
        "grade.stack_compat_score": sc.get("score"),
        "grade.snr": iq.get("snr"),
        "grade.noise_adu": iq.get("noise_adu"),
        "grade.saturation_fraction": iq.get("saturation_fraction"),
        "grade.fwhm_arcsec": iq.get("fwhm_arcsec"),
        "grade.fwhm_source": iq.get("fwhm_source"),
        "grade.star_pixels": iq.get("star_pixels"),
        "grade.exptime_ratio": tf.get("exptime_ratio"),
        "grade.filter_match": tf.get("filter_match"),
        "grade.filter_requested": tf.get("filter_requested"),
        "grade.filter_actual": tf.get("filter_actual"),
        "grade.calstat": tf.get("calstat"),
        "grade.capture_latency_sec": tl.get("capture_latency_sec"),
        "grade.upload_duration_sec": tl.get("upload_duration_sec"),
        "grade.points_base": pb.get("base_points"),
        "grade.points_quality_mult": pb.get("quality_multiplier"),
        "grade.points_exptime_factor": pb.get("exptime_factor"),
        "grade.points_timeliness_factor": pb.get("timeliness_factor"),
        "grade.points_tenure_mult": pb.get("tenure_multiplier"),
    }
    rep = grade.get("reputation_partial") or grade.get("reputation_stats") or {}
    if not rep and ctx is not None:
        # Sidecar may pass telescopes.reputation_stats on context (#28)
        ctx_rep = ctx.get("reputation_stats")
        if isinstance(ctx_rep, dict):
            rep = ctx_rep
    if rep:
        mapping = {
            "grade.reliability_ema": "reliability_score",
            "grade.task_quality_ema": "task_quality_score",
            "grade.reputation_points_per_hour": "points_per_hour",
            "grade.reputation_total_points": "total_points",
            "grade.reputation_frame_count": "frame_count",
            "grade.reputation_total_exposure": "total_exposure_seconds",
        }
        for metric_id, key in mapping.items():
            val = rep.get(key)
            if val is not None:
                out[metric_id] = val
    return {k: v for k, v in out.items() if v is not None}


def _grade_fits(path: str, ctx: EntityContext) -> dict[str, typing.Any] | None:
    try:
        from worker.grading import grade_frame
    except ImportError:
        try:
            from grading.fits_reader import read_sp_headers
            from grading.orchestrate import build_grade_payload
            from saucepan_pipeline import quality
        except ImportError:
            return None

        task_context = {
            k: ctx[k]
            for k in (
                "upload_id",
                "task_id",
                "telescope_id",
                "assignment_sent_at",
                "upload_completed_at",
                "upload_started_at",
                "integration_time_requested",
                "filter_requested",
                "predicted_psf_arcsec",
                "max_psf_fwhm",
                "contrib_pixscale",
                "max_resolution",
                "idempotency_key",
            )
            if ctx.get(k) is not None
        }
        qm = quality.assess_fits(path, update_fits=False)
        headers = read_sp_headers(path)
        return build_grade_payload(
            task_context,
            quality_metrics=qm,
            headers=headers,
            grader_version="metrics-sidecar",
        )

    task_context = {
        k: ctx[k]
        for k in (
            "upload_id",
            "task_id",
            "telescope_id",
            "assignment_sent_at",
            "upload_completed_at",
            "upload_started_at",
            "integration_time_requested",
            "filter_requested",
            "predicted_psf_arcsec",
            "max_psf_fwhm",
            "contrib_pixscale",
            "max_resolution",
            "idempotency_key",
        )
        if ctx.get(k) is not None
    }
    return grade_frame(path, task_context, update_fits=False)
