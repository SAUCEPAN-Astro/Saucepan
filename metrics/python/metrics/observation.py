"""Observation envelope — wire format for all metrics."""

from __future__ import annotations

import typing
import uuid
from datetime import datetime, timezone

SCHEMA_VERSION = "1"


class EntityContext(typing.TypedDict, total=False):
    node_id: str
    telescope_id: str
    upload_id: str
    frame_id: str
    task_id: str
    campaign_id: str
    night_id: str
    target_id: str
    staged_path: str
    clock_source: str
    detector_temp_c: float
    telescope_snapshot: dict[str, typing.Any]
    node_cache: dict[str, typing.Any]
    frame_catalog: list[dict[str, typing.Any]]
    _session_rollup: dict[str, typing.Any]
    _network_rollup: dict[str, typing.Any]
    _timing: dict[str, typing.Any]
    _ops: dict[str, typing.Any]
    user_id: str
    observer_display_name: str


def new_observation_id() -> str:
    return str(uuid.uuid4())


def utc_now_iso() -> str:
    return datetime.now(timezone.utc).isoformat()


class Observation(typing.TypedDict):
    schema_version: str
    observation_id: str
    entity_type: str
    entity_id: str
    producer: str
    observed_at: str
    metrics: dict[str, typing.Any]
    context: EntityContext
    wait_pile: list[str]


def build_observation(
    *,
    producer: str,
    entity_type: str,
    entity_id: str,
    context: EntityContext,
    metrics: dict[str, typing.Any],
    wait_pile: list[str] | None = None,
) -> Observation:
    return {
        "schema_version": SCHEMA_VERSION,
        "observation_id": new_observation_id(),
        "entity_type": entity_type,
        "entity_id": entity_id,
        "producer": producer,
        "observed_at": utc_now_iso(),
        "metrics": metrics,
        "context": context,
        "wait_pile": wait_pile or [],
    }
