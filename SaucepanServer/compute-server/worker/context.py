"""Task context keys passed from upload/SQS events into grade_frame."""

from __future__ import annotations

import json
import os
import typing
from pathlib import Path

TASK_CONTEXT_KEYS: tuple[str, ...] = (
    "upload_id",
    "task_id",
    "telescope_id",
    "assignment_sent_at",
    "upload_completed_at",
    "upload_time",
    "upload_started_at",
    "integration_time_requested",
    "filter_requested",
    "predicted_psf_arcsec",
    "idempotency_key",
    "allow_emulator",
    "telescope_is_emulator",
)

# Identity fields that must match a staged sidecar when one is present.
PROVENANCE_IDENTITY_KEYS: tuple[str, ...] = ("upload_id", "task_id")

# Adjacent JSON: ``frame.fits`` → ``frame.fits.sp_task.json``
STAGED_TASK_SIDECAR_SUFFIX = ".sp_task.json"


def resolve_staged_path(path: str | Path, *, must_exist: bool = True) -> Path:
    """Resolve a worker input under ``STORAGE_ROOT`` and reject traversal."""
    root = Path(os.environ.get("STORAGE_ROOT", "/data")).resolve()
    candidate = Path(path)
    if not candidate.is_absolute():
        candidate = root / candidate
    resolved = candidate.resolve()
    if root not in resolved.parents and resolved != root:
        raise ValueError(f"path outside STORAGE_ROOT: {path}")
    if must_exist and not resolved.is_file():
        raise FileNotFoundError(f"staged FITS not found: {resolved}")
    return resolved


def extract_task_context(event: typing.Mapping[str, typing.Any]) -> dict[str, typing.Any]:
    """Build task_context dict from an event, omitting keys with None values."""
    return {key: event[key] for key in TASK_CONTEXT_KEYS if event.get(key) is not None}


def staged_task_sidecar_path(fits_path: str | Path) -> Path:
    """Return the optional provenance sidecar path beside a staged FITS file."""
    return Path(f"{fits_path}{STAGED_TASK_SIDECAR_SUFFIX}")


def load_staged_task_sidecar(fits_path: str | Path) -> dict[str, typing.Any] | None:
    """Load and allowlist-filter a staged task sidecar, or None if missing."""
    path = staged_task_sidecar_path(fits_path)
    if not path.is_file():
        return None
    with path.open(encoding="utf-8") as fh:
        raw = json.load(fh)
    if not isinstance(raw, dict):
        raise ValueError(f"staged task sidecar must be a JSON object: {path}")
    return extract_task_context(raw)


def apply_staged_provenance(
    fits_path: str | Path,
    client_context: typing.Mapping[str, typing.Any],
) -> dict[str, typing.Any]:
    """
    Merge allowlisted client context with staged sidecar identity.

    When a sidecar exists, client ``upload_id`` / ``task_id`` that disagree
    with the sidecar are rejected (forged override). Sidecar identity wins.
    When no sidecar is present, identity fields supplied by the client are
    rejected. Non-identity metadata may still be passed through; the caller
    must obtain upload/task identity from the trusted staged sidecar.
    """
    filtered = extract_task_context(client_context)
    sidecar = load_staged_task_sidecar(fits_path)
    if sidecar is None:
        missing_binding = [key for key in PROVENANCE_IDENTITY_KEYS if key in filtered]
        if missing_binding:
            raise ValueError(
                "staged task sidecar required for "
                + ", ".join(missing_binding)
            )
        return filtered

    for key in PROVENANCE_IDENTITY_KEYS:
        if key not in sidecar or key not in filtered:
            continue
        if str(filtered[key]) != str(sidecar[key]):
            raise ValueError(f"task_context.{key} does not match staged sidecar provenance")

    merged = dict(filtered)
    for key in PROVENANCE_IDENTITY_KEYS:
        if key in sidecar:
            merged[key] = sidecar[key]
    return merged
