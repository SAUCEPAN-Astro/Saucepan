"""normalize/prov_header.py — inline SP_PROV JSON provenance."""

from __future__ import annotations

import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))

from normalize.prov_header import build_prov_payload, compact_prov_json, prov_uri


def test_build_prov_payload_basic_fields() -> None:
    payload = build_prov_payload(
        source="live", norm_version="0.2.0", checksum="a" * 64, tier=1, wcs_used=True
    )
    assert payload["src"] == "live"
    assert payload["norm"] == "0.2.0"
    assert payload["chks"] == "a" * 64
    assert payload["tier"] == 1
    assert payload["wcs"] is True
    assert "n" not in payload


def test_build_prov_payload_truncates_long_checksum() -> None:
    payload = build_prov_payload(
        source="live", norm_version="0.2.0", checksum="b" * 200, tier=1, wcs_used=False
    )
    assert len(payload["chks"]) == 72


def test_build_prov_payload_includes_resolved_key_count() -> None:
    payload = build_prov_payload(
        source="archive",
        norm_version="0.2.0",
        checksum="c" * 10,
        tier=2,
        wcs_used=False,
        resolved_keys=["SP_RA", "SP_DEC", "SP_TELE"],
    )
    assert payload["n"] == 3


def test_build_prov_payload_empty_resolved_keys_omits_n() -> None:
    payload = build_prov_payload(
        source="unknown",
        norm_version="0.2.0",
        checksum="d",
        tier=3,
        wcs_used=False,
        resolved_keys=[],
    )
    assert "n" not in payload


def test_compact_prov_json_short_payload_unchanged() -> None:
    payload = {"src": "live", "norm": "0.2.0", "chks": "abc", "tier": 1, "wcs": True}
    text = compact_prov_json(payload)
    assert json.loads(text) == payload


def test_compact_prov_json_truncates_checksum_when_over_budget() -> None:
    payload = {
        "src": "live",
        "norm": "0.2.0",
        "chks": "f" * 500,
        "tier": 1,
        "wcs": True,
        "n": 10,
    }
    text = compact_prov_json(payload, max_chars=280)
    assert len(text) <= 280
    parsed = json.loads(text)
    assert len(parsed["chks"]) < 500


def test_compact_prov_json_drops_n_when_still_over_budget() -> None:
    payload = {
        "src": "x" * 50,
        "norm": "0.2.0",
        "chks": "y" * 500,
        "tier": 1,
        "wcs": True,
        "n": 999,
    }
    text = compact_prov_json(payload, max_chars=60)
    assert len(text) <= 60


def test_compact_prov_json_hard_truncates_as_last_resort() -> None:
    payload = {"src": "z" * 1000, "norm": "0.2.0", "chks": "w" * 1000, "tier": 1, "wcs": True}
    text = compact_prov_json(payload, max_chars=50)
    assert len(text) <= 50


def test_prov_uri_returns_fits_header_uri() -> None:
    assert prov_uri() == "fits://header#SP_PROV"
