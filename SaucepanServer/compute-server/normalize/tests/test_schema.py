"""normalize/schema.py — tier computation boundary behavior."""

from __future__ import annotations

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))

from normalize.schema import MANDATORY_HEADERS, SP_HEADERS, compute_tier


def test_mandatory_headers_match_documented_tier1_set() -> None:
    # Per CLAUDE.md's contract: SP_RA, SP_DEC, SP_TELE, SP_FILTER, SP_EXPTIME, SP_DATEOBS
    expected = {"SP_RA", "SP_DEC", "SP_TELE", "SP_FILTER", "SP_EXPTIME", "SP_DATEOBS"}
    assert set(MANDATORY_HEADERS) == expected


def test_compute_tier_all_resolved_is_tier1() -> None:
    total = len(MANDATORY_HEADERS)
    assert compute_tier(total, total) == 1


def test_compute_tier_none_resolved_is_tier3() -> None:
    assert compute_tier(0, len(MANDATORY_HEADERS)) == 3


def test_compute_tier_zero_total_mandatory_is_tier3() -> None:
    assert compute_tier(0, 0) == 3


def test_compute_tier_boundary_at_80_percent() -> None:
    # 5/6 = 0.833 >= 0.8 -> tier 1
    assert compute_tier(5, 6) == 1
    # 4/6 = 0.667 -> tier 2 (>= 0.4)
    assert compute_tier(4, 6) == 2


def test_compute_tier_boundary_at_40_percent() -> None:
    # exactly 40% -> tier 2
    assert compute_tier(2, 5) == 2
    # just under 40% -> tier 3
    assert compute_tier(1, 5) == 3


def test_sp_headers_schema_has_description_and_type_for_every_entry() -> None:
    for key, (typ, desc, mandatory) in SP_HEADERS.items():
        assert key.startswith("SP_")
        assert typ in ("str", "float", "int")
        assert isinstance(desc, str) and desc
        assert isinstance(mandatory, bool)
