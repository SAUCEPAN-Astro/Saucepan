"""Locate the metrics contract files in a checkout or an installed wheel."""

from __future__ import annotations

import sysconfig
from pathlib import Path


def contract_path(filename: str) -> Path:
    """Return a contract path for source checkouts and installed packages."""
    source_path = Path(__file__).resolve().parents[2] / "contracts" / filename
    target_path = (
        Path(__file__).resolve().parents[1]
        / "share"
        / "saucepan-metrics"
        / "contracts"
        / filename
    )
    installed_path = (
        Path(sysconfig.get_path("data"))
        / "share"
        / "saucepan-metrics"
        / "contracts"
        / filename
    )
    if source_path.is_file():
        return source_path
    if target_path.is_file():
        return target_path
    return installed_path
