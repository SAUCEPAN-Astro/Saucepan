"""Projector protocol."""

from __future__ import annotations

import typing

from metrics.observation import Observation


class Projector(typing.Protocol):
    def apply(self, observation: Observation) -> None: ...
