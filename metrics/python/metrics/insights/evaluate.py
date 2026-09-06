"""Insight rule evaluation — sidecar-only, log+alert on breach."""

from __future__ import annotations

import functools
import logging
import operator
import pathlib
import re
import typing

import yaml

from metrics._contracts import contract_path
from metrics.observation import Observation, build_observation

logger = logging.getLogger(__name__)

_RULES_PATH = contract_path("insight_rules.yaml")

_WHEN_RE = re.compile(
    r"^\s*(?P<metric>[a-zA-Z0-9_.]+)\s*(?P<op>>=|<=|==|!=|>|<)\s*(?P<value>.+)\s*$"
)

_OPS = {
    ">": operator.gt,
    ">=": operator.ge,
    "<": operator.lt,
    "<=": operator.le,
    "==": operator.eq,
    "!=": operator.ne,
}


@functools.lru_cache(maxsize=1)
def load_insight_rules(path: pathlib.Path | None = None) -> list[dict[str, typing.Any]]:
    rules_path = path or _RULES_PATH
    if not rules_path.is_file():
        return []
    with rules_path.open(encoding="utf-8") as fh:
        raw = yaml.safe_load(fh) or {}
    return list(raw.get("rules") or [])


def _parse_value(raw: str) -> typing.Any:
    text = raw.strip()
    if (text.startswith("'") and text.endswith("'")) or (
        text.startswith('"') and text.endswith('"')
    ):
        return text[1:-1]
    try:
        if "." in text:
            return float(text)
        return int(text)
    except ValueError:
        return text


def _resolve_metric(metrics: dict[str, typing.Any], metric_id: str) -> typing.Any:
    if metric_id in metrics:
        return metrics[metric_id]
    alt = metric_id.replace(".", "_")
    return metrics.get(alt)


def _match_when(when: str, metrics: dict[str, typing.Any]) -> bool:
    match = _WHEN_RE.match(when)
    if not match:
        return False
    metric_id = match.group("metric")
    op = match.group("op")
    expected = _parse_value(match.group("value"))
    actual = _resolve_metric(metrics, metric_id)
    if actual is None:
        return False
    fn = _OPS.get(op)
    if fn is None:
        return False
    try:
        return bool(fn(actual, expected))
    except TypeError:
        return False


def evaluate(
    observations: Observation | list[Observation],
    *,
    rules: list[dict[str, typing.Any]] | None = None,
) -> list[Observation]:
    """
    Evaluate insight rules against observation metric payloads.

    Breach handling is log+alert only — no blocking, no scheduler side effects.
    """
    rule_list = rules if rules is not None else load_insight_rules()
    obs_list = observations if isinstance(observations, list) else [observations]
    insights: list[Observation] = []

    for obs in obs_list:
        metrics = dict(obs.get("metrics") or {})
        context = dict(obs.get("context") or {})
        for rule in rule_list:
            if rule.get("enabled") is False:
                continue
            when = str(rule.get("when") or "")
            if not when or not _match_when(when, metrics):
                continue

            evidence = {
                "rule_id": rule.get("id"),
                "when": when,
                "metrics": {
                    k: metrics[k] for k in metrics if k in when or k.startswith("session.")
                },
            }
            attach = rule.get("attach") or []
            for key in attach:
                if key in context:
                    evidence[key] = context[key]

            event_type = str(rule.get("emit") or rule.get("id") or "INSIGHT")
            severity = str(rule.get("severity") or "info")

            logger.warning(
                "insight breach rule=%s emit=%s severity=%s",
                rule.get("id"),
                event_type,
                severity,
            )

            insights.append(
                build_observation(
                    producer="insight_evaluator",
                    entity_type="insight",
                    entity_id=str(rule.get("id") or event_type),
                    context=context,
                    metrics={
                        "insight.event_type": event_type,
                        "insight.severity": severity,
                        "insight.evidence": evidence,
                    },
                    wait_pile=obs.get("wait_pile") or [],
                )
            )

    return insights
