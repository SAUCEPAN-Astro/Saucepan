"""Campaign product / time-resolution routing (#422).

Stacking is optional time-binning for depth campaigns. Photometry / time-domain
campaigns default to calibrated frames + photometry table via ``/v1/photometry``,
not ``POST /v1/stack``.
"""

from __future__ import annotations

import typing

MODES = frozenset({"per_frame", "time_bin", "stack"})
DEFAULT_MODE = "per_frame"


def product_from_ctx(ctx: dict[str, typing.Any] | None) -> dict[str, typing.Any]:
    """Extract ``product`` from task/campaign context (pack or flat keys)."""
    if not ctx:
        return {}
    prod = ctx.get("product")
    if isinstance(prod, dict):
        return prod
    snap = ctx.get("task_snapshot") or ctx.get("pack") or {}
    if isinstance(snap, dict):
        nested = snap.get("product")
        if isinstance(nested, dict):
            return nested
    mode = ctx.get("product_mode") or ctx.get("mode")
    if mode:
        out: dict[str, typing.Any] = {"mode": mode}
        if ctx.get("time_bin_frames") is not None:
            out["time_bin_frames"] = ctx["time_bin_frames"]
        return out
    return {}


def normalize_mode(product: dict[str, typing.Any] | None) -> str:
    """Return product mode; unset → ``per_frame`` (photometry default)."""
    if not product:
        return DEFAULT_MODE
    mode = str(product.get("mode") or DEFAULT_MODE).strip().lower()
    if mode not in MODES:
        return DEFAULT_MODE
    return mode


def wants_stack(product: dict[str, typing.Any] | None = None, *, ctx: dict | None = None) -> bool:
    """True only for depth campaigns that opt into full ``/v1/stack``."""
    prod = product if product is not None else product_from_ctx(ctx)
    return normalize_mode(prod) == "stack"


def route_for_product(
    product: dict[str, typing.Any] | None = None,
    *,
    ctx: dict | None = None,
) -> str:
    """
    Compute route label for a pack/product.

    Returns:
        ``photometry`` — per-frame or time_bin (calibrated frames + table; not stack)
        ``stack`` — full imaging stack via ``/v1/stack``
    """
    if wants_stack(product, ctx=ctx):
        return "stack"
    return "photometry"


def validate_product(product: dict[str, typing.Any] | None) -> str | None:
    """Return error message or None if valid / empty."""
    if not product:
        return None
    mode = str(product.get("mode") or DEFAULT_MODE).strip().lower()
    if mode not in MODES:
        return f"product.mode must be one of {sorted(MODES)}"
    bins = product.get("time_bin_frames")
    if mode == "time_bin":
        try:
            n = int(bins)
        except (TypeError, ValueError):
            return "product.time_bin_frames must be an integer >= 2 when mode=time_bin"
        if n < 2:
            return "product.time_bin_frames must be >= 2 when mode=time_bin"
    elif bins not in (None, 0, ""):
        return "product.time_bin_frames is only valid when mode=time_bin"
    return None
