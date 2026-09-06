"""Producer: upload / frame context IDs (no FITS read)."""

from __future__ import annotations

from metrics.observation import EntityContext

_CLOCK_SOURCES = frozenset({"UTC", "NTP", "GPS", "UNKNOWN"})


def _normalize_clock_source(raw: object) -> str:
    val = str(raw or "UNKNOWN").strip().upper()
    return val if val in _CLOCK_SOURCES else "UNKNOWN"


def produce(ctx: EntityContext) -> dict[str, object]:
    out: dict[str, object] = {}
    mapping = {
        "frame.upload_id": "upload_id",
        "frame.frame_id": "frame_id",
        "frame.campaign_id": "campaign_id",
        "frame.task_id": "task_id",
        "frame.telescope_id": "telescope_id",
    }
    for metric_id, key in mapping.items():
        val = ctx.get(key)  # type: ignore[arg-type]
        if val is not None:
            out[metric_id] = val

    clock = ctx.get("clock_source")
    if clock is not None:
        out["frame.timesys"] = _normalize_clock_source(clock)

    temp = ctx.get("detector_temp_c")
    if temp is not None:
        out["frame.detector_temp"] = temp

    return out
