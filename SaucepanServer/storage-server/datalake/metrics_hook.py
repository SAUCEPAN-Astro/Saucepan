"""Datalake adapter — ONLY file that couples metrics sidecar to catalog DB."""

from __future__ import annotations

import logging
import os
import sys
import time
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

logger = logging.getLogger(__name__)

# Metrics package + metrics_store.py live under top-level metrics/python/ (#426
# consolidation, was SaucepanServer/compute-server/metrics/). Plug: this is the
# only place that couples the datalake to the metrics sidecar, so it's the one
# sys.path shim that needs to know the new location.
_METRICS_PKG = Path(__file__).resolve().parents[3] / "metrics" / "python"
if (_METRICS_PKG / "metrics").is_dir() and str(_METRICS_PKG) not in sys.path:
    sys.path.insert(0, str(_METRICS_PKG))


def _night_id(telescope_id: str | None, date_obs: str | None) -> str | None:
    if not telescope_id:
        return None
    day = (date_obs or datetime.now(timezone.utc).isoformat())[:10]
    return f"{telescope_id}_{day}"


def _build_context(upload_id: str) -> dict[str, Any] | None:
    from catalog import Frame, Upload
    from db import session_scope

    with session_scope() as session:
        upload = session.get(Upload, upload_id)
        if upload is None:
            return None
        frame = (
            session.query(Frame)
            .filter(Frame.upload_id == upload_id)
            .order_by(Frame.created_at.desc())
            .first()
        )
        if frame is None:
            return None

        catalog_snap = {
            "upload_id": upload.id,
            "upload_status": upload.status,
            "size_bytes": upload.size_bytes,
            "etag": upload.etag,
            "checksum_sha256": frame.checksum_sha256,
            "staged_path": frame.staged_path,
            "grade_status": frame.grade_status,
            "headline_grade": frame.headline_grade,
            "ingest_status": frame.ingest_status,
        }
        meta = dict(upload.metadata_json or {})
        task_snapshot = meta.get("task_snapshot") or {}
        timing: dict[str, object] = dict(meta.get("timing") or {})
        ctx: dict[str, Any] = {
            "upload_id": upload.id,
            "frame_id": frame.id,
            "campaign_id": upload.campaign_id,
            "task_id": upload.task_id or meta.get("task_id"),
            "telescope_id": upload.telescope_id or meta.get("telescope_id"),
            "node_id": meta.get("node_id"),
            "staged_path": frame.staged_path,
            "assignment_sent_at": meta.get("assignment_sent_at"),
            "upload_completed_at": meta.get("upload_completed_at")
            or upload.completed_at.isoformat()
            if upload.completed_at
            else None,
            "upload_started_at": meta.get("upload_started_at"),
            "integration_time_requested": meta.get("integration_time_requested"),
            "filter_requested": meta.get("filter_requested"),
            "predicted_psf_arcsec": meta.get("predicted_psf_arcsec"),
            "idempotency_key": meta.get("idempotency_key"),
            "task_snapshot": task_snapshot,
            "campaign_comp_stars": meta.get("campaign_comp_stars")
            or task_snapshot.get("comp_stars"),
            "telescope_snapshot": meta.get("telescope_snapshot"),
            "node_cache": meta.get("node_cache"),
            "reputation_stats": meta.get("reputation_stats"),
            "max_psf_fwhm": task_snapshot.get("max_psf_fwhm_arcsec"),
            "contrib_pixscale": task_snapshot.get("contrib_pixscale"),
            "max_resolution": task_snapshot.get("max_resolution_arcsec"),
            "_timing": timing,
            "_catalog": catalog_snap,
        }
        if frame.grade_json:
            ctx["_grade_result"] = frame.grade_json
            rep = frame.grade_json.get("reputation_partial") or frame.grade_json.get(
                "reputation_stats"
            )
            if rep:
                ctx["reputation_stats"] = rep
        ctx["night_id"] = _night_id(ctx.get("telescope_id"), meta.get("date_obs"))
        ctx["clock_source"] = meta.get("clock_source")
        ctx["detector_temp_c"] = meta.get("detector_temp_c")

        # Ensure telescope_snapshot has enough static fields for node_profile (#22)
        if not ctx.get("telescope_snapshot") and upload.telescope_id:
            snap = dict(meta.get("telescope") or {})
            snap.setdefault("telescope_id", upload.telescope_id)
            for key in (
                "aperture_mm",
                "focal_length_mm",
                "pixel_size_um",
                "site_latitude",
                "site_longitude",
                "median_seeing_arcsec",
                "fov_width_arcmin",
                "fov_height_arcmin",
                "mount_type",
                "available_filters",
            ):
                if meta.get(key) is not None:
                    snap.setdefault(key, meta[key])
            if snap:
                ctx["telescope_snapshot"] = snap

        # Feed L4 network rollup when campaign has catalog rows (#23)
        if upload.campaign_id:
            try:
                from storage.frame_catalog import list_for_campaign

                ctx["frame_catalog"] = list_for_campaign(upload.campaign_id)
            except Exception:
                logger.exception("frame_catalog list failed campaign_id=%s", upload.campaign_id)
                ctx["frame_catalog"] = []

        # L2 session rollup for this telescope/night (#21)
        tele = ctx.get("telescope_id")
        night = ctx.get("night_id")
        if tele and night:
            try:
                from metrics_store import list_l1_frames_for_night
                from metrics.projectors.session import rollup_night

                night_frames = list_l1_frames_for_night(str(tele), str(night))
                if night_frames:
                    ctx["_session_rollup"] = rollup_night(
                        str(tele), str(night), frames=night_frames
                    )
            except Exception:
                logger.exception("session rollup failed telescope_id=%s night_id=%s", tele, night)
        return ctx


def _enrich_photometry(ctx: dict[str, Any]) -> None:
    """Run photometry on compute-server; fail-open."""
    path = ctx.get("staged_path")
    if not path or os.getenv("METRICS_PHOTOMETRY", "1").lower() in ("0", "false", "off"):
        return
    try:
        from compute_client import request_photometry
    except ImportError:
        return

    task_context = {
        k: ctx[k]
        for k in (
            "upload_id",
            "task_id",
            "telescope_id",
            "campaign_comp_stars",
        )
        if ctx.get(k) is not None
    }
    t0 = time.perf_counter()
    try:
        summary = request_photometry(
            str(path), task_context, run_lp=bool(ctx.get("campaign_comp_stars"))
        )
        ctx["_photometry_result"] = summary
        if isinstance(summary.get("lp"), dict):
            ctx["_lp_result"] = summary["lp"]
        timing = ctx.setdefault("_timing", {})
        if isinstance(timing, dict):
            timing["photometry_duration_ms"] = round((time.perf_counter() - t0) * 1000, 1)
    except Exception:
        logger.exception("photometry failed upload_id=%s", ctx.get("upload_id"))


def _save_observation(observation: dict[str, Any]) -> None:
    from metrics_store import save_metric_observation

    save_metric_observation(observation)


def notify_upload_complete(upload_id: str) -> None:
    """Fail-open hook for staging.py — never raises."""
    if os.getenv("METRICS_SIDECAR", "1").lower() in ("0", "false", "off"):
        return
    try:
        from metrics.sidecar import dispatch
    except ImportError:
        logger.warning("metrics package not installed; skip sidecar upload_id=%s", upload_id)
        return

    ctx = _build_context(upload_id)
    if ctx is None:
        logger.warning("metrics sidecar: no context for upload_id=%s", upload_id)
        return

    try:
        _enrich_photometry(ctx)
    except Exception:
        logger.exception("metrics photometry enrich failed upload_id=%s", upload_id)

    try:
        dispatch(ctx, save_fn=_save_observation)
    except Exception:
        logger.exception("metrics sidecar notify failed upload_id=%s", upload_id)


def notify_stack_complete(
    summary: dict[str, Any],
    output_path: str,
    *,
    telescope_id: str | None = None,
    campaign_id: str | None = None,
) -> None:
    """Fail-open hook after stack — idempotent on output_path entity id."""
    if os.getenv("METRICS_SIDECAR", "1").lower() in ("0", "false", "off"):
        return
    try:
        from metrics.sidecar import dispatch_stack
    except ImportError:
        logger.warning("metrics package not installed; skip stack sidecar path=%s", output_path)
        return

    ctx: dict[str, Any] = {
        "stack_output_path": output_path,
        "_stack_summary": summary,
        "telescope_id": telescope_id,
        "campaign_id": campaign_id,
    }
    try:
        dispatch_stack(ctx, save_fn=_save_observation)
    except Exception:
        logger.exception("metrics stack sidecar failed path=%s", output_path)


def list_wait_pile(upload_id: str) -> list[str]:
    """Return wait-pile metric ids recorded for an upload (debug/Q&A)."""
    from metrics_store import list_wait_pile_for_upload

    return list_wait_pile_for_upload(upload_id)
