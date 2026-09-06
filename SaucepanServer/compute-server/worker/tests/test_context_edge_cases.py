"""Edge cases for worker.context sidecar loading / provenance merge."""

from __future__ import annotations

import json

import pytest
from worker.context import (
    STAGED_TASK_SIDECAR_SUFFIX,
    apply_staged_provenance,
    extract_task_context,
    load_staged_task_sidecar,
)


def test_extract_task_context_empty_event():
    assert extract_task_context({}) == {}


def test_extract_task_context_drops_none_values():
    filtered = extract_task_context({"upload_id": None, "task_id": "t1"})
    assert filtered == {"task_id": "t1"}


def test_load_staged_task_sidecar_missing_returns_none(tmp_path):
    fits = tmp_path / "frame.fits"
    fits.write_bytes(b"stub")
    assert load_staged_task_sidecar(fits) is None


def test_load_staged_task_sidecar_non_dict_raises(tmp_path):
    fits = tmp_path / "frame.fits"
    fits.write_bytes(b"stub")
    sidecar = tmp_path / f"frame.fits{STAGED_TASK_SIDECAR_SUFFIX}"
    sidecar.write_text(json.dumps(["not", "a", "dict"]), encoding="utf-8")

    with pytest.raises(ValueError, match="must be a JSON object"):
        load_staged_task_sidecar(fits)


def test_apply_staged_provenance_no_sidecar_keeps_non_identity_context(tmp_path):
    fits = tmp_path / "frame.fits"
    fits.write_bytes(b"stub")
    result = apply_staged_provenance(fits, {"telescope_id": "t1", "campaign_id": "forged"})
    assert result == {"telescope_id": "t1"}


def test_apply_staged_provenance_no_sidecar_rejects_identity(tmp_path):
    fits = tmp_path / "frame.fits"
    fits.write_bytes(b"stub")

    with pytest.raises(ValueError, match="sidecar required"):
        apply_staged_provenance(fits, {"upload_id": "u1"})


def test_apply_staged_provenance_merges_when_keys_match(tmp_path):
    fits = tmp_path / "frame.fits"
    fits.write_bytes(b"stub")
    sidecar = tmp_path / f"frame.fits{STAGED_TASK_SIDECAR_SUFFIX}"
    sidecar.write_text(
        json.dumps({"upload_id": "real-upload", "task_id": "real-task"}),
        encoding="utf-8",
    )
    merged = apply_staged_provenance(
        fits, {"upload_id": "real-upload", "task_id": "real-task", "telescope_id": "t1"}
    )
    assert merged["upload_id"] == "real-upload"
    assert merged["task_id"] == "real-task"
    assert merged["telescope_id"] == "t1"


def test_apply_staged_provenance_sidecar_only_key_still_applied(tmp_path):
    """Sidecar carries upload_id but client omits it — sidecar value wins/merges in."""
    fits = tmp_path / "frame.fits"
    fits.write_bytes(b"stub")
    sidecar = tmp_path / f"frame.fits{STAGED_TASK_SIDECAR_SUFFIX}"
    sidecar.write_text(json.dumps({"upload_id": "sidecar-upload"}), encoding="utf-8")

    merged = apply_staged_provenance(fits, {"task_id": "t1"})
    assert merged["upload_id"] == "sidecar-upload"
    assert merged["task_id"] == "t1"


def test_apply_staged_provenance_client_only_key_not_overwritten(tmp_path):
    """Sidecar present but doesn't carry task_id — client's task_id is untouched."""
    fits = tmp_path / "frame.fits"
    fits.write_bytes(b"stub")
    sidecar = tmp_path / f"frame.fits{STAGED_TASK_SIDECAR_SUFFIX}"
    sidecar.write_text(json.dumps({"upload_id": "sidecar-upload"}), encoding="utf-8")

    merged = apply_staged_provenance(
        fits, {"upload_id": "sidecar-upload", "task_id": "client-task"}
    )
    assert merged["task_id"] == "client-task"
