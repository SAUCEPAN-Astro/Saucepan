"""Realized continuous-coverage metrics from delivery records.

Not a science oracle: geometric night / airmass are not modeled in v1.
Prefer server ``GET .../coverage/status`` when available; this helper works on
local delivery sidecars after inbox ack.
"""

from __future__ import annotations

from collections.abc import Mapping, Sequence
from dataclasses import dataclass, field
from datetime import datetime, timezone
from typing import Any


def _parse_ts(value: Any) -> datetime | None:
    if value is None:
        return None
    if isinstance(value, datetime):
        ts = value
    else:
        s = str(value).strip()
        if not s:
            return None
        if s.endswith("Z"):
            s = s[:-1] + "+00:00"
        try:
            ts = datetime.fromisoformat(s)
        except ValueError:
            return None
    if ts.tzinfo is None:
        ts = ts.replace(tzinfo=timezone.utc)
    return ts.astimezone(timezone.utc)


def circular_lon_span_deg(lons: Sequence[float]) -> float:
    """Minimum arc (degrees) covering longitudes on a circle."""
    if not lons:
        return 0.0
    if len(lons) == 1:
        return 0.0
    norm = sorted((lon % 360.0 + 360.0) % 360.0 for lon in lons)
    max_gap = 0.0
    for i in range(len(norm) - 1):
        max_gap = max(max_gap, norm[i + 1] - norm[i])
    wrap = (norm[0] + 360.0) - norm[-1]
    max_gap = max(max_gap, wrap)
    span = 360.0 - max_gap
    return max(0.0, span)


@dataclass
class CoverageMetrics:
    """KPI snapshot for continuous seasons."""

    status: str = "insufficient_data"  # ok | degraded | failed | insufficient_data
    gate_reasons: list[str] = field(default_factory=list)
    contributing_telescopes: dict[str, int] = field(default_factory=dict)
    longitude_span_deg: float | None = None
    realized_max_gap_min: float | None = None
    duty_cycle: float = 0.0
    bin_minutes: float = 15.0
    vs_intent: dict[str, Any] = field(default_factory=dict)
    sample_count: int = 0

    def to_dict(self) -> dict[str, Any]:
        return {
            "status": self.status,
            "gate_reasons": list(self.gate_reasons),
            "contributing_telescopes": dict(self.contributing_telescopes),
            "longitude_span_deg": self.longitude_span_deg,
            "realized_max_gap_min": self.realized_max_gap_min,
            "duty_cycle": self.duty_cycle,
            "bin_minutes": self.bin_minutes,
            "vs_intent": dict(self.vs_intent),
            "sample_count": self.sample_count,
        }


def compute_coverage_metrics(
    deliveries: Sequence[Any],
    *,
    window_start: datetime | None = None,
    window_end: datetime | None = None,
    max_gap_intent_min: float | None = None,
    target_duty_cycle: float | None = None,
    min_longitude_span_deg: float | None = None,
    min_sites: int | None = None,
    mode: str = "soft",
    telescope_longitudes: Mapping[str, float] | None = None,
    bin_minutes: float = 15.0,
    stack_eligible_only: bool = False,
    completed_only: bool = True,
) -> CoverageMetrics:
    """Compute duty cycle, realized max gap, and longitude span from deliveries."""
    hard = str(mode).lower() == "hard"
    samples: list[tuple[datetime, str]] = []
    counts: dict[str, int] = {}

    for d in deliveries:
        if isinstance(d, Mapping):
            data = d
        else:
            data = {
                "created_at": getattr(d, "created_at", None),
                "telescope_id": getattr(d, "telescope_id", None),
                "status": getattr(d, "status", None),
                "stack_eligible": getattr(d, "stack_eligible", None),
            }
        if completed_only:
            st = str(data.get("status") or "completed").lower()
            if st and st not in ("completed", "complete", ""):
                continue
        if stack_eligible_only and data.get("stack_eligible") is False:
            continue
        ts = _parse_ts(data.get("created_at"))
        if ts is None:
            continue
        if window_start and ts < window_start:
            continue
        if window_end and ts > window_end:
            continue
        tel = str(data.get("telescope_id") or "") or "unknown"
        samples.append((ts, tel))
        counts[tel] = counts.get(tel, 0) + 1

    samples.sort(key=lambda x: x[0])
    metrics = CoverageMetrics(
        bin_minutes=bin_minutes,
        contributing_telescopes=dict(counts),
        sample_count=len(samples),
    )
    reasons: list[str] = []

    lons: list[float] = []
    if telescope_longitudes:
        for tel in counts:
            if tel in telescope_longitudes:
                lons.append(float(telescope_longitudes[tel]))
        if len(lons) >= 2:
            metrics.longitude_span_deg = circular_lon_span_deg(lons)
        elif len(lons) == 1:
            metrics.longitude_span_deg = 0.0

    if len(samples) >= 2:
        max_gap = 0.0
        for i in range(1, len(samples)):
            gap = (samples[i][0] - samples[i - 1][0]).total_seconds() / 60.0
            if gap > max_gap:
                max_gap = gap
        metrics.realized_max_gap_min = max_gap
        start, end = samples[0][0], samples[-1][0]
        if end > start and bin_minutes > 0:
            bins = max(1, int((end - start).total_seconds() / 60.0 / bin_minutes + 0.999999))
            filled: set[int] = set()
            for ts, _ in samples:
                idx = int((ts - start).total_seconds() / 60.0 / bin_minutes)
                idx = max(0, min(bins - 1, idx))
                filled.add(idx)
            metrics.duty_cycle = len(filled) / bins
        else:
            metrics.duty_cycle = 1.0
        metrics.status = "ok"
        if max_gap_intent_min is not None and max_gap > max_gap_intent_min:
            reasons.append("realized_max_gap_min exceeds intent")
            metrics.status = "failed" if hard else "degraded"
    elif len(samples) == 1:
        metrics.duty_cycle = 1.0
        metrics.status = "insufficient_data"
        reasons.append("need ≥2 frames for gap metric")
    else:
        reasons.append("no frames yet")

    if min_sites is not None and len(counts) < min_sites:
        reasons.append("min_sites not met")
        if hard:
            metrics.status = "failed"
        elif metrics.status == "ok":
            metrics.status = "degraded"

    if (
        min_longitude_span_deg is not None
        and metrics.longitude_span_deg is not None
        and metrics.longitude_span_deg + 1e-9 < min_longitude_span_deg
        and len(lons) >= 2
    ):
        reasons.append("min_longitude_span_deg not met")
        if hard:
            metrics.status = "failed"
        elif metrics.status == "ok":
            metrics.status = "degraded"

    vs: dict[str, Any] = {
        "max_gap_intent_min": max_gap_intent_min,
        "target_duty_cycle": target_duty_cycle,
        "min_longitude_span_deg": min_longitude_span_deg,
        "min_sites": min_sites,
        "mode": mode,
    }
    if metrics.realized_max_gap_min is not None and max_gap_intent_min is not None:
        vs["max_gap_excess_min"] = metrics.realized_max_gap_min - max_gap_intent_min
    if target_duty_cycle is not None:
        vs["duty_cycle_delta"] = metrics.duty_cycle - target_duty_cycle
    metrics.vs_intent = vs
    metrics.gate_reasons = reasons
    return metrics


def metrics_from_pack(
    deliveries: Sequence[Any],
    pack: Any,
    *,
    telescope_longitudes: Mapping[str, float] | None = None,
    **kwargs: Any,
) -> CoverageMetrics:
    """Pull intent from pack.coverage + pack.season (dict or CampaignPack)."""
    if hasattr(pack, "to_dict"):
        data = pack.to_dict()
    elif isinstance(pack, Mapping):
        data = pack
    else:
        data = {}
    cov = data.get("coverage") or {}
    season = data.get("season") or {}
    window_start = _parse_ts(season.get("window_start"))
    window_end = _parse_ts(season.get("window_end"))
    return compute_coverage_metrics(
        deliveries,
        window_start=window_start,
        window_end=window_end,
        max_gap_intent_min=cov.get("max_gap_min"),
        target_duty_cycle=season.get("target_duty_cycle"),
        min_longitude_span_deg=cov.get("min_longitude_span_deg")
        or season.get("min_longitude_span_deg"),
        min_sites=cov.get("min_sites"),
        mode=str(cov.get("mode") or "soft"),
        telescope_longitudes=telescope_longitudes,
        **kwargs,
    )
