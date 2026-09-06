"""Load metric registry; separate live vs wait pile."""

from __future__ import annotations

import functools
import pathlib
from dataclasses import dataclass

import yaml

from metrics._contracts import contract_path

_REGISTRY_PATH = contract_path("registry.yaml")


@dataclass(frozen=True)
class MetricSpec:
    id: str
    status: str
    layer: str
    producer: str | None = None
    wait_reason: str | None = None


def _default_registry_path() -> pathlib.Path:
    env = __import__("os").environ.get("METRICS_REGISTRY_PATH")
    if env:
        return pathlib.Path(env)
    return _REGISTRY_PATH


@functools.lru_cache(maxsize=1)
def load_registry(path: pathlib.Path | None = None) -> dict[str, MetricSpec]:
    reg_path = path or _default_registry_path()
    with reg_path.open(encoding="utf-8") as fh:
        raw = yaml.safe_load(fh)
    out: dict[str, MetricSpec] = {}
    for row in raw.get("metrics") or []:
        spec = MetricSpec(
            id=row["id"],
            status=row["status"],
            layer=row.get("layer", ""),
            producer=row.get("producer"),
            wait_reason=row.get("wait_reason"),
        )
        out[spec.id] = spec
    return out


def live_metric_ids(registry: dict[str, MetricSpec] | None = None) -> frozenset[str]:
    reg = registry or load_registry()
    return frozenset(mid for mid, s in reg.items() if s.status == "live")


def wait_metrics(registry: dict[str, MetricSpec] | None = None) -> list[MetricSpec]:
    reg = registry or load_registry()
    return [s for s in reg.values() if s.status == "wait"]


def producers_for_status(
    status: str, registry: dict[str, MetricSpec] | None = None
) -> frozenset[str]:
    reg = registry or load_registry()
    names: set[str] = set()
    for spec in reg.values():
        if spec.status == status and spec.producer:
            names.add(spec.producer)
    return frozenset(names)
