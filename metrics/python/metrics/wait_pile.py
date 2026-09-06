"""Wait pile — metrics deferred pending Q&A (generated subset)."""

from __future__ import annotations

import yaml

from metrics._contracts import contract_path

WAIT_PILE_PATH = contract_path("wait_pile.yaml")


def load_wait_pile() -> list[dict]:
    with WAIT_PILE_PATH.open(encoding="utf-8") as fh:
        doc = yaml.safe_load(fh)
    return list(doc.get("metrics") or [])
