"""Tests for inline SP_PROV header serialization."""

from __future__ import annotations

import json
import pathlib
import sys

# metrics/ moved out from under SaucepanServer/compute-server/ (#426 metrics
# consolidation); normalize/ stayed put, so reach it explicitly instead of
# assuming a co-located sibling.
REPO_ROOT = pathlib.Path(__file__).resolve().parents[4]
sys.path.insert(0, str(REPO_ROOT / "SaucepanServer" / "compute-server"))

from normalize.prov_header import (  # noqa: E402
    build_prov_payload,
    compact_prov_json,
    prov_uri,
)


def test_compact_prov_json_under_limit():
    payload = build_prov_payload(
        source="live",
        norm_version="0.2.0",
        checksum="sha256:" + "a" * 64,
        tier=1,
        wcs_used=True,
        resolved_keys=["SP_RA", "SP_DEC"],
    )
    text = compact_prov_json(payload, max_chars=280)
    assert len(text) <= 280
    parsed = json.loads(text)
    assert parsed["src"] == "live"
    assert parsed["tier"] == 1


def test_prov_uri_points_to_header():
    assert prov_uri() == "fits://header#SP_PROV"
