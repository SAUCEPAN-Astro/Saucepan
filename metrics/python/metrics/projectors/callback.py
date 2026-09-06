"""Persist observations via callback (datalake injects store)."""

from __future__ import annotations

import typing

from metrics.observation import Observation


class CallbackProjector:
    """Thin adapter — datalake passes a save function; metrics package stays DB-free."""

    def __init__(self, save_fn: typing.Callable[[Observation], None]) -> None:
        self._save = save_fn

    def apply(self, observation: Observation) -> None:
        self._save(observation)
