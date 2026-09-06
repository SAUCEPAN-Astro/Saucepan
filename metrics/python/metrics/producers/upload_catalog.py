"""Producer: datalake upload / frame catalog fields."""

from __future__ import annotations

import typing

from metrics.observation import EntityContext


def produce(ctx: EntityContext) -> dict[str, typing.Any]:
    catalog = ctx.get("_catalog")
    if not isinstance(catalog, dict):
        return _from_ctx(ctx)
    out: dict[str, typing.Any] = {}
    mapping = {
        "ops.upload_id": "upload_id",
        "ops.upload_status": "upload_status",
        "ops.upload_size_bytes": "size_bytes",
        "ops.upload_etag": "etag",
        "ops.checksum_sha256": "checksum_sha256",
        "ops.frame_grade_status": "grade_status",
        "ops.frame_headline_grade": "headline_grade",
        "ops.ingest_status": "ingest_status",
    }
    for metric_id, key in mapping.items():
        val = catalog.get(key)
        if val is not None:
            out[metric_id] = val
    return out


def _from_ctx(ctx: EntityContext) -> dict[str, typing.Any]:
    out: dict[str, typing.Any] = {}
    if ctx.get("upload_id"):
        out["ops.upload_id"] = ctx["upload_id"]
    return out
