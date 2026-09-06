"""Run producers and emit observations."""

from __future__ import annotations

import hashlib
import logging
import typing

from metrics.observation import EntityContext, Observation, build_observation
from metrics.privacy import sanitize_observation
from metrics.registry import load_registry, wait_metrics

logger = logging.getLogger(__name__)

ProducerFn = typing.Callable[[EntityContext], dict[str, typing.Any]]


def _wait_pile_ids() -> list[str]:
    return [spec.id for spec in wait_metrics()]


def run_producer(
    name: str,
    fn: ProducerFn,
    ctx: EntityContext,
    *,
    entity_type: str = "frame",
    entity_id_key: str = "upload_id",
) -> Observation | None:
    try:
        metrics = fn(ctx)
    except Exception:
        logger.exception("producer %s failed", name)
        return None
    if not metrics:
        return None
    entity_id = ctx.get(entity_id_key)
    if entity_id is None:
        entity_id = ctx.get("frame_id")
    stack_path = False
    if entity_id is None:
        entity_id = ctx.get("stack_output_path")
        stack_path = entity_id is not None
    if entity_id is not None and (entity_id_key == "stack_output_path" or stack_path):
        entity_id = hashlib.sha256(str(entity_id).encode("utf-8")).hexdigest()[:16]
    entity_id = entity_id or "unknown"
    return sanitize_observation(build_observation(
        producer=name,
        entity_type=entity_type,
        entity_id=str(entity_id),
        context=ctx,
        metrics=metrics,
        wait_pile=_wait_pile_ids(),
    ))


def run_all_producers(
    ctx: EntityContext,
    producers: dict[str, ProducerFn],
    allowed: frozenset[str] | None = None,
) -> list[Observation]:
    """Run each producer in isolation; collect observations."""
    reg = load_registry()
    live_producers = {
        spec.producer for spec in reg.values() if spec.status == "live" and spec.producer
    }
    if allowed is not None:
        live_producers &= allowed

    observations: list[Observation] = []
    for name, fn in producers.items():
        if name not in live_producers:
            continue
        obs = run_producer(name, fn, ctx)
        if obs is not None:
            observations.append(obs)
    return observations


def run_stack_producers(
    ctx: EntityContext,
    producers: dict[str, ProducerFn],
) -> list[Observation]:
    """Run stack_summary producer only (stack entity)."""
    reg = load_registry()
    if "stack_summary" not in {
        spec.producer for spec in reg.values() if spec.status == "live" and spec.producer
    }:
        return []

    fn = producers.get("stack_summary")
    if fn is None:
        return []
    obs = run_producer(
        "stack_summary",
        fn,
        ctx,
        entity_type="stack",
        entity_id_key="stack_output_path",
    )
    return [obs] if obs is not None else []
