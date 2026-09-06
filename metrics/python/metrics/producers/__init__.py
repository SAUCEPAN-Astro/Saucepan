"""Registered producers (domain → module)."""

from __future__ import annotations

import typing

from metrics.observation import EntityContext
from metrics.producers import (
    frame_headers,
    frame_photometry,
    governance,
    grade,
    lp_products,
    network_rollup,
    node_profile,
    service_health,
    session_rollup,
    stack_summary,
    task_snapshot,
    upload_catalog,
    upload_context,
)

ProducerFn = typing.Callable[[EntityContext], dict[str, typing.Any]]

PRODUCERS: dict[str, ProducerFn] = {
    "frame_headers": frame_headers.produce,
    "frame_photometry": frame_photometry.produce,
    "upload_context": upload_context.produce,
    "grade": grade.produce,
    "upload_catalog": upload_catalog.produce,
    "governance": governance.produce,
    "stack_summary": stack_summary.produce,
    "task_snapshot": task_snapshot.produce,
    "lp_products": lp_products.produce,
    "service_health": service_health.produce,
    "node_profile": node_profile.produce,
    "session_rollup": session_rollup.produce,
    "network_rollup": network_rollup.produce,
}
