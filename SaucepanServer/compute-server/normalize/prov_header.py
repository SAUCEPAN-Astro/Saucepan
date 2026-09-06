"""
Inline provenance JSON for FITS header SP_PROV.
"""

from __future__ import annotations

import json
import typing

_MAX_PROV_CHARS = 280
_PROV_URI = "fits://header#SP_PROV"


def build_prov_payload(
    *,
    source: str,
    norm_version: str,
    checksum: str,
    tier: int,
    wcs_used: bool,
    resolved_keys: typing.Sequence[str] | None = None,
) -> dict[str, typing.Any]:
    """Compact provenance record for inline header storage."""
    payload: dict[str, typing.Any] = {
        "src": source,
        "norm": norm_version,
        "chks": checksum[:72],
        "tier": tier,
        "wcs": bool(wcs_used),
    }
    if resolved_keys:
        payload["n"] = len(resolved_keys)
    return payload


def compact_prov_json(payload: dict[str, typing.Any], max_chars: int = _MAX_PROV_CHARS) -> str:
    """
    Serialize provenance to compact JSON, truncating checksum if needed.

    Astropy writes values longer than 80 chars using FITS CONTINUE cards.
    """
    text = json.dumps(payload, separators=(",", ":"))
    if len(text) <= max_chars:
        return text

    trimmed = dict(payload)
    chks = str(trimmed.get("chks", ""))
    if chks:
        overhead = len(json.dumps({**trimmed, "chks": ""}, separators=(",", ":")))
        budget = max(16, max_chars - overhead - 2)
        trimmed["chks"] = chks[:budget]
        text = json.dumps(trimmed, separators=(",", ":"))

    if len(text) > max_chars:
        trimmed.pop("n", None)
        text = json.dumps(trimmed, separators=(",", ":"))

    return text[:max_chars]


def prov_uri() -> str:
    """Metric value for frame.prov_uri when SP_PROV is written inline."""
    return _PROV_URI
