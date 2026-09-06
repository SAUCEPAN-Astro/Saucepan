"""SLO threshold checks — notify-only, never blocks main path."""

from __future__ import annotations

import functools
import logging
import pathlib
import typing

import yaml

from metrics._contracts import contract_path

logger = logging.getLogger(__name__)

_CONFIG_PATH = contract_path("slo_config.yaml")

NotifyEvent = dict[str, typing.Any]


@functools.lru_cache(maxsize=1)
def load_slo_config(path: pathlib.Path | None = None) -> dict[str, typing.Any]:
    cfg_path = path or _CONFIG_PATH
    if not cfg_path.is_file():
        return {"version": 1, "rules": []}
    with cfg_path.open(encoding="utf-8") as fh:
        return yaml.safe_load(fh) or {"version": 1, "rules": []}


def _breach(value: typing.Any, threshold: typing.Any, op: str) -> bool:
    try:
        val = float(value)
        limit = float(threshold)
    except (TypeError, ValueError):
        if op == "eq":
            return value == threshold
        if op == "ne":
            return value != threshold
        return False

    if op == "gt":
        return val > limit
    if op == "gte":
        return val >= limit
    if op == "lt":
        return val < limit
    if op == "lte":
        return val <= limit
    if op == "eq":
        return val == limit
    if op == "ne":
        return val != limit
    return False


def observations_to_metric_map(
    observations: list[typing.Any] | dict[str, typing.Any],
) -> dict[str, typing.Any]:
    """Flatten Observation list (or pass-through dict) into metric_id → value."""
    if isinstance(observations, dict):
        return observations
    out: dict[str, typing.Any] = {}
    for obs in observations or []:
        if not isinstance(obs, dict):
            continue
        metrics = obs.get("metrics")
        if isinstance(metrics, dict):
            out.update(metrics)
    return out


def check_slos(
    metrics: dict[str, typing.Any] | list[typing.Any],
    *,
    config: dict[str, typing.Any] | None = None,
) -> list[NotifyEvent]:
    """
    Compare metric values against ``slo_config.yaml`` thresholds.

    Accepts a flat metric dict **or** a list of Observation envelopes (#368).
    Returns notify events for downstream alerting. Fail-open: errors log and
    return an empty list — never raises, never blocks callers.
    """
    try:
        flat = observations_to_metric_map(metrics)
        cfg = config if config is not None else load_slo_config()
        events: list[NotifyEvent] = []
        for rule in cfg.get("rules") or []:
            metric_id = rule.get("metric")
            if not metric_id or metric_id not in flat:
                continue
            threshold = rule.get("threshold")
            if threshold is None:
                continue
            op = str(rule.get("op", "gt"))
            if not _breach(flat[metric_id], threshold, op):
                continue
            events.append(
                {
                    "event": "slo_breach",
                    "metric": metric_id,
                    "value": flat[metric_id],
                    "threshold": threshold,
                    "op": op,
                    "window": rule.get("window"),
                    "action": rule.get("action", "log_alert"),
                    "severity": rule.get("severity", "warn"),
                }
            )
        return events
    except Exception:
        logger.exception("SLO check failed (fail-open)")
        return []
