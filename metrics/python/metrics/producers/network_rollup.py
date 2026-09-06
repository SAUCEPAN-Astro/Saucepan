"""Producer: network.* metrics from campaign rollup (stub until frame_catalog)."""

from __future__ import annotations

import typing

from metrics.observation import EntityContext
from metrics.projectors.network import rollup_network


def produce(ctx: EntityContext) -> dict[str, typing.Any]:
    rollup = ctx.get("_network_rollup")
    if rollup is None:
        campaign_id = ctx.get("campaign_id")
        if not campaign_id:
            return {}
        catalog = ctx.get("frame_catalog")
        if not isinstance(catalog, list):
            return {}
        rollup = rollup_network(str(campaign_id), frame_catalog=catalog)

    if not isinstance(rollup, dict) or not rollup:
        return {}

    return {
        key: val for key, val in rollup.items() if key.startswith("network.") and val is not None
    }
