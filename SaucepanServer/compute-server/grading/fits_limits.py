"""
FITS dimension and file-size guards before pixel materialization.

Used by normalize checksum/output paths and grade/stack paths to reject
crafted FITS with inflated NAXIS keywords or oversized on-disk payloads
before loading ``hdul[0].data`` into RAM.
"""

from __future__ import annotations

import hashlib
import os
from pathlib import Path
from typing import Any

# ~8000×8000 float32 frame; override via MAX_FITS_PIXELS.
DEFAULT_MAX_FITS_PIXELS = 64_000_000
# On-disk cap catches gzip-tile bombs; override via MAX_FITS_BYTES.
DEFAULT_MAX_FITS_BYTES = 512 * 1024 * 1024

# Chunk size for streaming checksum over memmap-backed arrays.
CHECKSUM_CHUNK_BYTES = 8 * 1024 * 1024


class FitsSizeLimitError(ValueError):
    """Raised when a FITS file exceeds configured pixel or byte limits."""


def _env_int(name: str, default: int) -> int:
    raw = os.environ.get(name, "").strip()
    if not raw:
        return default
    try:
        return int(raw)
    except ValueError:
        return default


def max_fits_pixels() -> int:
    return _env_int("MAX_FITS_PIXELS", DEFAULT_MAX_FITS_PIXELS)


def max_fits_bytes() -> int:
    return _env_int("MAX_FITS_BYTES", DEFAULT_MAX_FITS_BYTES)


def pixel_count_from_header(hdr: Any) -> int:
    """Product of NAXISn from a FITS header without loading pixel data."""
    try:
        naxis = int(hdr.get("NAXIS", 0))
    except (TypeError, ValueError):
        return 0
    if naxis <= 0:
        return 0

    count = 1
    cap = max_fits_pixels()
    for axis in range(1, naxis + 1):
        key = f"NAXIS{axis}"
        try:
            dim = int(hdr[key])
        except (KeyError, TypeError, ValueError):
            return 0
        if dim <= 0:
            return 0
        count *= dim
        if count > cap:
            return count
    return count


def check_fits_file_size(path: str | os.PathLike[str]) -> None:
    """Raise FitsSizeLimitError if on-disk file exceeds MAX_FITS_BYTES."""
    p = Path(path)
    try:
        size = p.stat().st_size
    except OSError as exc:
        raise FitsSizeLimitError(f"Cannot stat FITS file: {p}") from exc

    cap = max_fits_bytes()
    if size > cap:
        raise FitsSizeLimitError(
            f"FITS file size {size:,} bytes exceeds MAX_FITS_BYTES={cap:,} ({p})"
        )


def check_fits_header_limits(hdr: Any, *, path: str | None = None) -> None:
    """Raise FitsSizeLimitError if header NAXIS product exceeds MAX_FITS_PIXELS."""
    pixels = pixel_count_from_header(hdr)
    cap = max_fits_pixels()
    if pixels > cap:
        suffix = f" ({path})" if path else ""
        raise FitsSizeLimitError(
            f"FITS pixel count {pixels:,} exceeds MAX_FITS_PIXELS={cap:,}{suffix}"
        )


def ensure_fits_loadable(path: str, hdr: Any) -> None:
    """Run byte and header pixel checks before accessing HDU pixel data."""
    check_fits_file_size(path)
    check_fits_header_limits(hdr, path=path)


def checksum_primary_data(data: Any, *, chunk_bytes: int = CHECKSUM_CHUNK_BYTES) -> str:
    """SHA-256 of primary HDU pixels without materializing the full array.

    Uses chunked reads so memmap-backed arrays stay bounded in RAM.
    """
    hasher = hashlib.sha256()
    if data is None:
        return "sha256:" + hasher.hexdigest()

    try:
        size = int(data.size)
    except (AttributeError, TypeError, ValueError):
        return "sha256:" + hasher.hexdigest()

    if size == 0:
        return "sha256:" + hasher.hexdigest()

    flat = data.ravel()
    itemsize = max(1, int(flat.itemsize))
    chunk_elems = max(1, chunk_bytes // itemsize)
    for start in range(0, size, chunk_elems):
        hasher.update(flat[start : start + chunk_elems].tobytes())

    return "sha256:" + hasher.hexdigest()
