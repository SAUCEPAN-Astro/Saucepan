"""Tunables for tiled / streaming stack combination (#414).

``combine.stack_frames`` used to allocate two full ``(n_frames, ny, nx)``
float64 cubes (plus several iterative-clip temporaries) - a working set of
~13x one such cube, measured. That is what pinned ``MAX_STACK_FRAMES`` at a
fixed 64. The combiner now walks the frame in ``STACK_TILE_PX`` tiles and
accumulates ``science`` / ``weight`` / ``variance`` / ``coverage`` a tile at
a time, so peak scales with tile *area*, not frame count, and the frame cap
becomes a memory budget instead of a magic number.

All three knobs are plain ints from the environment with a coded default:

* ``STACK_TILE_PX``        - tile edge in pixels (default 1024).
* ``STACK_MEM_BUDGET_MB``  - combiner accumulator budget (default 4096).
* ``MAX_STACK_FRAMES``     - optional hard override of the computed cap.
"""

from __future__ import annotations

import os

DEFAULT_STACK_TILE_PX = 1024
DEFAULT_STACK_MEM_BUDGET_MB = 4096
MIN_STACK_FRAMES = 8
HARD_MAX_STACK_FRAMES = 512

# Per-frame cost of one tile, as a proxy: two float64 planes (the value and
# the weight cube slabs). The real tiled working set is a small multiple of
# this - stacked-cube copies plus the iterative-clip temporaries - but that
# multiple is folded into the budget default rather than the formula, so the
# published cap stays a legible ``budget / (tile_px^2 * 16)``.
_TILE_BYTES_PER_FRAME = 16  # 2 * sizeof(float64)
# Retained per-pixel arrays during reprojection: float32 data, float64
# variance, and boolean validity/mask arrays. Keep this estimate conservative
# so the tile budget cannot be bypassed by a large frame cohort.
_REPROJECTED_BYTES_PER_FRAME_PIXEL = 16


def _env_int(name: str, default: int) -> int:
    raw = os.environ.get(name, "").strip()
    if not raw:
        return default
    try:
        value = int(raw)
    except ValueError:
        return default
    return value if value > 0 else default


def stack_tile_px() -> int:
    """Tile edge in pixels (``STACK_TILE_PX``, default 1024)."""
    return _env_int("STACK_TILE_PX", DEFAULT_STACK_TILE_PX)


def stack_mem_budget_mb() -> int:
    """Combiner accumulator budget in MB (``STACK_MEM_BUDGET_MB``, default 4096)."""
    return _env_int("STACK_MEM_BUDGET_MB", DEFAULT_STACK_MEM_BUDGET_MB)


def resolve_tile_px(tile_px: int | None = None) -> int:
    """Explicit ``tile_px`` when positive, else the ``STACK_TILE_PX`` value."""
    return tile_px if (tile_px and tile_px > 0) else stack_tile_px()


def max_stack_frames(
    tile_px: int | None = None, mem_budget_mb: int | None = None
) -> int:
    """Memory-aware cap on frames per stack (replaces the fixed 64).

    ``budget_bytes / (tile_px**2 * 16)``, clamped to
    ``[MIN_STACK_FRAMES, HARD_MAX_STACK_FRAMES]``. A positive
    ``MAX_STACK_FRAMES`` env var overrides the computation entirely (only
    the hard ceiling still applies) - handy for tests and for operators
    who want a flat number.
    """
    override = _env_int("MAX_STACK_FRAMES", 0)
    if override > 0:
        return min(HARD_MAX_STACK_FRAMES, override)

    tpx = resolve_tile_px(tile_px)
    budget_mb = (
        mem_budget_mb
        if (mem_budget_mb and mem_budget_mb > 0)
        else stack_mem_budget_mb()
    )
    computed = int(
        (budget_mb * 1024 * 1024) / (tpx * tpx * _TILE_BYTES_PER_FRAME)
    )
    return max(MIN_STACK_FRAMES, min(HARD_MAX_STACK_FRAMES, computed))


def should_stream(
    n_frames: int,
    ny: int,
    nx: int,
    tile_px: int | None = None,
    mem_budget_mb: int | None = None,
) -> bool:
    """True when even one tile's ``(n_frames, tile, tile)`` cube blows the budget.

    In that case ``stack_frames`` falls back to a single-pass streaming
    accumulate (one frame-tile at a time, no stacked cube, no cross-frame
    sigma-clip - a deliberate tradeoff).
    """
    tpx = resolve_tile_px(tile_px)
    th = min(tpx, ny)
    tw = min(tpx, nx)
    budget_mb = (
        mem_budget_mb
        if (mem_budget_mb and mem_budget_mb > 0)
        else stack_mem_budget_mb()
    )
    tile_cube_bytes = n_frames * th * tw * _TILE_BYTES_PER_FRAME
    return tile_cube_bytes > budget_mb * 1024 * 1024


def ensure_reprojection_memory_budget(
    n_frames: int,
    ny: int,
    nx: int,
    mem_budget_mb: int | None = None,
) -> None:
    """Reject cohorts whose retained reprojection arrays exceed the budget.

    Tiling bounds the combiner's per-tile cube, but reprojection currently
    retains each frame's data, variance, validity, and exclusion mask until
    combination. Check that working set before allocating any of those
    arrays.
    """
    budget_mb = (
        mem_budget_mb
        if (mem_budget_mb and mem_budget_mb > 0)
        else stack_mem_budget_mb()
    )
    estimated_bytes = (
        max(0, int(n_frames))
        * max(0, int(ny))
        * max(0, int(nx))
        * _REPROJECTED_BYTES_PER_FRAME_PIXEL
    )
    budget_bytes = budget_mb * 1024 * 1024
    if estimated_bytes > budget_bytes:
        raise ValueError(
            "stack reprojection working set exceeds STACK_MEM_BUDGET_MB: "
            f"estimated {estimated_bytes:,} bytes for {n_frames} frames, "
            f"budget {budget_bytes:,} bytes"
        )
