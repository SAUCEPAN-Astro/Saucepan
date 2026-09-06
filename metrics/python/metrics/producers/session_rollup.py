"""Producer: session.* metrics from rollup projector output."""

from __future__ import annotations

import typing

from metrics.observation import EntityContext


def produce(ctx: EntityContext) -> dict[str, typing.Any]:
    rollup = ctx.get("_session_rollup")
    if not isinstance(rollup, dict):
        return {}
    return {
        key: val
        for key, val in rollup.items()
        if val is not None and (key.startswith("session.") or key == "frame.extinction_coeff")
    }
