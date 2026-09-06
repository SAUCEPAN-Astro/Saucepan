"""Producer: task definition snapshot embedded in upload metadata."""

from __future__ import annotations

import typing

from metrics.observation import EntityContext

_TASK_METRICS: dict[str, str] = {
    "task.name": "name",
    "task.integration_time": "integration_time",
    "task.min_power": "min_power",
    "task.required_filters": "required_filters",
    "task.max_psf_fwhm": "max_psf_fwhm_arcsec",
    "task.min_resolution": "min_resolution_arcsec",
    "task.max_resolution": "max_resolution_arcsec",
    "task.min_psf_band": "min_psf_band",
    "task.max_psf_band": "max_psf_band",
    "task.min_seeing_power": "min_seeing_power",
    "task.max_seeing_power": "max_seeing_power",
    "task.target_ra": "target_ra",
    "task.target_dec": "target_dec",
    "task.min_altitude": "min_altitude_deg",
    "task.required_fov": "required_fov",
    "task.priority": "priority",
    "task.original_priority": "original_priority",
    "task.status": "status",
    "task.assigned_tele_id": "assigned_telescope_id",
    "task.scheduled_end": "scheduled_end",
    "task.handoff_lead": "handoff_lead",
    "task.user_end": "user_end",
    "task.min_visibility": "min_visibility",
    "task.obs_horizon": "obs_horizon",
    "task.science_band": "science_band",
    "task.campaign_id": "campaign_id",
    "task.paths": "paths",
    "task.processing_job_id": "processing_job_id",
    "task.contrib_fwhm": "contrib_fwhm",
    "task.contrib_pixscale": "contrib_pixscale",
    "task.contrib_status": "contrib_status",
    "task.predicted_psf": "predicted_psf",
    "task.cohort_match_score": "cohort_match_score",
}


def produce(ctx: EntityContext) -> dict[str, typing.Any]:
    snap = ctx.get("task_snapshot")
    if not isinstance(snap, dict) or not snap:
        return {}

    out: dict[str, typing.Any] = {}
    for metric_id, field in _TASK_METRICS.items():
        val = snap.get(field)
        if val is None and field.endswith("_arcsec"):
            val = snap.get(field.replace("_arcsec", ""))
        if val is None and field == "min_altitude_deg":
            val = snap.get("min_altitude")
        if val is None and field == "assigned_telescope_id":
            val = snap.get("assigned_tele_id")
        if val is not None:
            out[metric_id] = val
    return out
