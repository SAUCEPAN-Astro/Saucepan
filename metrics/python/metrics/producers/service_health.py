"""Producer: service health ops.* from hook timing + ops context (#370)."""

from __future__ import annotations

import typing

from metrics.observation import EntityContext

_TIMING_METRICS: dict[str, str] = {
    "grade_duration_ms": "ops.grade_duration_ms",
    "stack_duration_ms": "ops.stack_duration_ms",
    "normalize_duration_ms": "ops.normalize_duration_ms",
    "ingest_duration_ms": "ops.ingest_duration_ms",
}

_OPS_CONTEXT: dict[str, str] = {
    "task_failure_count": "ops.task_failure_count",
    "task_acc_exptime": "ops.task_acc_exptime",
    "last_assign_attempt": "ops.last_assign_attempt",
    "handoff_requested": "ops.handoff_requested",
    "scope_heartbeat": "ops.scope_heartbeat",
    "prometheus_export": "ops.prometheus_export",
}


def produce(ctx: EntityContext) -> dict[str, typing.Any]:
    out: dict[str, typing.Any] = {}

    campaign_id = ctx.get("campaign_id")
    if campaign_id:
        out["ops.campaign_id"] = campaign_id

    timing = ctx.get("_timing")
    if isinstance(timing, dict):
        for src_key, metric_id in _TIMING_METRICS.items():
            val = timing.get(src_key)
            if val is not None:
                out[metric_id] = val

        errors = timing.get("errors")
        if errors:
            out["ops.svc_status"] = "degraded"
            out["ops.compute_error"] = "compute_error"
        elif out or timing:
            out.setdefault("ops.svc_status", "ok")

        for breach_key, metric_id in (
            ("grade_duration_p95_breach", "ops.grade_duration_p95_breach"),
            ("orch_latency_p99_breach", "ops.orch_latency_p99_breach"),
            ("notify_latency_p99_breach", "ops.notify_latency_p99_breach"),
        ):
            if timing.get(breach_key) is True:
                out[metric_id] = True

        for src_key, metric_id in _OPS_CONTEXT.items():
            val = timing.get(src_key)
            if val is not None:
                out[metric_id] = val

    ops = ctx.get("_ops")
    if isinstance(ops, dict):
        for src_key, metric_id in _OPS_CONTEXT.items():
            if metric_id in out:
                continue
            val = ops.get(src_key)
            if val is not None:
                out[metric_id] = val
        if ops.get("campaign_id") and "ops.campaign_id" not in out:
            out["ops.campaign_id"] = ops["campaign_id"]

    return out
