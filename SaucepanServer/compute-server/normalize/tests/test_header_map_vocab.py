"""header_map/vocab.py — synonym-based header mapper (the live path used by
normalize.normalize_fits). Covers camera synonym edge cases, transforms, and
the physical-domain validators that silently drop out-of-range mappings.
"""

from __future__ import annotations

import sys
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))

from normalize.header_map.vocab import (
    _canonicalize_filter,
    _to_dec_deg,
    _to_ra_deg,
    _to_utc_iso,
    apply_vocab,
    load_vocab,
)

# --- load_vocab -------------------------------------------------------


def test_load_vocab_reads_builtin_synonyms() -> None:
    vocab = load_vocab()
    assert "SP_RA" in vocab
    assert "EXPTIME" in vocab["SP_EXPTIME"]["synonyms"]


def test_load_vocab_merges_new_key_from_extra(tmp_path: Path) -> None:
    extra = tmp_path / "extra.yaml"
    extra.write_text("SP_TGTNAM:\n  synonyms: [MYNEWKEY]\n  transform: str\n")
    vocab = load_vocab(str(extra))
    assert "MYNEWKEY" in vocab["SP_TGTNAM"]["synonyms"]
    # Original synonyms preserved too.
    assert "OBJECT" in vocab["SP_TGTNAM"]["synonyms"]


def test_load_vocab_merges_brand_new_sp_key(tmp_path: Path) -> None:
    extra = tmp_path / "extra.yaml"
    extra.write_text("SP_CUSTOM:\n  synonyms: [WEIRDCAM_FIELD]\n")
    vocab = load_vocab(str(extra))
    assert vocab["SP_CUSTOM"]["synonyms"] == ["WEIRDCAM_FIELD"]


def test_load_vocab_extra_path_nonexistent_is_ignored(tmp_path: Path) -> None:
    missing = tmp_path / "does_not_exist.yaml"
    vocab = load_vocab(str(missing))
    assert "SP_RA" in vocab  # builtin still loaded


def test_load_vocab_dedupes_merged_synonyms(tmp_path: Path) -> None:
    extra = tmp_path / "extra.yaml"
    extra.write_text("SP_EXPTIME:\n  synonyms: [EXPTIME]\n  transform: float\n")
    vocab = load_vocab(str(extra))
    assert vocab["SP_EXPTIME"]["synonyms"].count("EXPTIME") == 1


# --- apply_vocab: basic mapping + first-match-wins ---------------------


def test_apply_vocab_maps_known_synonym() -> None:
    vocab = load_vocab()
    headers = {"EXPTIME": "30.0"}
    resolved = apply_vocab(headers, vocab)
    assert resolved["SP_EXPTIME"] == 30.0


def test_apply_vocab_first_synonym_in_list_wins() -> None:
    # SP_RA synonyms = [RA, OBJCTRA, RA_DEG, ...] — RA should win over OBJCTRA.
    vocab = load_vocab()
    headers = {"RA": "10.0", "OBJCTRA": "99.0"}
    resolved = apply_vocab(headers, vocab)
    assert resolved["SP_RA"] == 10.0


def test_apply_vocab_falls_through_to_second_synonym_when_first_absent() -> None:
    vocab = load_vocab()
    headers = {"OBJCTRA": "45.0"}
    resolved = apply_vocab(headers, vocab)
    assert resolved["SP_RA"] == 45.0


def test_apply_vocab_new_camera_via_extra_vocab(tmp_path: Path) -> None:
    """Simulates 'adding a new camera = edit synonyms.yaml' without touching
    any pipeline code — the documented extension path."""
    extra = tmp_path / "newcam.yaml"
    extra.write_text("SP_EXPTIME:\n  synonyms: [WEIRDCAM_EXP]\n  transform: float\n")
    vocab = load_vocab(str(extra))
    headers = {"WEIRDCAM_EXP": "12.5"}
    resolved = apply_vocab(headers, vocab)
    assert resolved["SP_EXPTIME"] == 12.5


def test_apply_vocab_empty_headers_returns_empty() -> None:
    vocab = load_vocab()
    assert apply_vocab({}, vocab) == {}


def test_apply_vocab_transform_exception_drops_mapping() -> None:
    vocab = load_vocab()
    headers = {"EXPTIME": "not-a-number"}
    resolved = apply_vocab(headers, vocab)
    assert "SP_EXPTIME" not in resolved


# --- apply_vocab: physical-domain validators ---------------------------


def test_apply_vocab_drops_ra_out_of_range() -> None:
    vocab = load_vocab()
    headers = {"RA": "999.0"}  # > 360
    resolved = apply_vocab(headers, vocab)
    assert "SP_RA" not in resolved


def test_apply_vocab_drops_dec_out_of_range() -> None:
    vocab = load_vocab()
    headers = {"DEC": "-95.0"}  # < -90
    resolved = apply_vocab(headers, vocab)
    assert "SP_DEC" not in resolved


def test_apply_vocab_accepts_boundary_ra_values() -> None:
    vocab = load_vocab()
    resolved = apply_vocab({"RA": "0.0"}, vocab)
    assert resolved["SP_RA"] == 0.0
    resolved = apply_vocab({"RA": "360.0"}, vocab)
    assert resolved["SP_RA"] == 360.0


def test_apply_vocab_drops_negative_exptime() -> None:
    vocab = load_vocab()
    resolved = apply_vocab({"EXPTIME": "-5.0"}, vocab)
    assert "SP_EXPTIME" not in resolved


def test_apply_vocab_drops_zero_exptime() -> None:
    vocab = load_vocab()
    resolved = apply_vocab({"EXPTIME": "0.0"}, vocab)
    assert "SP_EXPTIME" not in resolved


def test_apply_vocab_falls_through_when_first_synonym_fails_validation() -> None:
    """RA's first synonym (RA) is out of range and gets dropped by the
    validator via `continue` (not `break`), so apply_vocab tries the next
    synonym in the list (OBJCTRA) and recovers a valid value from it."""
    vocab = load_vocab()
    headers = {"RA": "999.0", "OBJCTRA": "45.0"}
    resolved = apply_vocab(headers, vocab)
    assert resolved["SP_RA"] == 45.0


def test_apply_vocab_drops_pixscale_out_of_range() -> None:
    vocab = load_vocab()
    resolved = apply_vocab({"PIXSCALE": "5000.0"}, vocab)
    assert "SP_PIXSCALE" not in resolved


# --- transforms ----------------------------------------------------------


def test_to_utc_iso_from_iso_string() -> None:
    result = _to_utc_iso("2024-06-15T12:00:00")
    assert result.startswith("2024-06-15")
    assert result.endswith("Z")


def test_to_utc_iso_from_mjd() -> None:
    # MJD 60000 -> 2023-02-25
    result = _to_utc_iso("60000.0")
    assert result.startswith("2023-02-25")


def test_to_utc_iso_from_jd() -> None:
    result = _to_utc_iso("2460000.5")
    assert "T" in result and result.endswith("Z")


def test_to_ra_deg_decimal() -> None:
    assert _to_ra_deg("180.5") == pytest.approx(180.5)


def test_to_ra_deg_sexagesimal_colon() -> None:
    # 12:00:00 hourangle -> 180 deg
    assert _to_ra_deg("12:00:00") == pytest.approx(180.0, abs=0.01)


def test_to_ra_deg_sexagesimal_space() -> None:
    assert _to_ra_deg("12 00 00") == pytest.approx(180.0, abs=0.01)


def test_to_dec_deg_decimal() -> None:
    assert _to_dec_deg("-45.25") == pytest.approx(-45.25)


def test_to_dec_deg_sexagesimal_colon() -> None:
    assert _to_dec_deg("-45:30:00") == pytest.approx(-45.5, abs=0.01)


def test_canonicalize_filter_known_aliases() -> None:
    assert _canonicalize_filter("h-alpha") == "Ha"
    assert _canonicalize_filter("OIII") == "OIII"
    assert _canonicalize_filter("oiii") == "OIII"
    assert _canonicalize_filter("clear") == "L"
    assert _canonicalize_filter("bayer") == "OSC"


def test_canonicalize_filter_unknown_passes_through_raw() -> None:
    assert _canonicalize_filter("SomeWeirdFilter") == "SomeWeirdFilter"


def test_apply_vocab_filter_canon_end_to_end() -> None:
    vocab = load_vocab()
    resolved = apply_vocab({"FILTER": "H-Alpha"}, vocab)
    assert resolved["SP_FILTER"] == "Ha"
