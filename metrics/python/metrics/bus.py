"""Metric bus — swappable transport."""

from __future__ import annotations

import threading
import typing

from metrics.observation import Observation

if typing.TYPE_CHECKING:
    from metrics.projectors.base import Projector


class MetricBus(typing.Protocol):
    def publish(self, observation: Observation) -> None: ...


class InProcessBus:
    """Alpha default: synchronous projector chain in caller thread."""

    def __init__(self, projectors: list[Projector] | None = None) -> None:
        self._projectors = list(projectors or [])
        self._published: list[Observation] = []

    def add_projector(self, projector: Projector) -> None:
        self._projectors.append(projector)

    @property
    def published(self) -> list[Observation]:
        return list(self._published)

    def publish(self, observation: Observation) -> None:
        self._published.append(observation)
        for projector in self._projectors:
            try:
                projector.apply(observation)
            except Exception:
                import logging

                logging.getLogger(__name__).exception(
                    "projector %s failed", type(projector).__name__
                )


class BackgroundThreadBus:
    """Fire-and-forget wrapper for main-path hooks."""

    def __init__(self, inner: MetricBus) -> None:
        self._inner = inner

    def publish(self, observation: Observation) -> None:
        threading.Thread(
            target=self._inner.publish,
            args=(observation,),
            daemon=True,
        ).start()
