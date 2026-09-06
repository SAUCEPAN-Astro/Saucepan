"""Weighted sigma-clip stacking of reprojected frames.

Combination is photon-noise-weighted and now per-pixel: each frame's
weight map is ``valid / var_pixel``, where ``var_pixel`` is the frame's
per-pixel variance reprojected onto the common grid (falling back to the
background-RMS estimate, then the scalar ``frame.noise_adu**2`` when no
per-pixel map exists). Uncovered edges carry ``valid = False`` and so
contribute exactly zero to both the value sum and the variance sum -
they are excluded by the mask, not by weight arithmetic that happens to
cancel.

Correlated-noise approximation: flux-conserving reprojection
(``reproject_exact``) resamples each output pixel from several input
pixels, so neighbouring output pixels share input noise and are
correlated. This v1 propagates only the per-pixel (white) variance term
and does not carry a covariance matrix or a red-noise inflation factor.
The residual correlated ("red") noise term sigma_red is the same one
quantified for time-series photometry by Pont, Zucker & Queloz (2006,
MNRAS 373, 231); folding it into the stacked noise_map is left to a
later issue in this lane.

Memory (#414): the value/weight/variance accumulators are built one
``STACK_TILE_PX`` tile at a time rather than as full ``(n_frames, ny, nx)``
float64 cubes, so peak working set scales with tile area, not frame count.
The per-pixel iterative clip (#411) and the mask fold (#413) are entirely
per-pixel along the frame axis, so a tiled run is bit-identical to the
whole-frame reference (``test_stack_tiled_parity.py``). For pathological
frame counts, where even one tile's ``(n_frames, tile, tile)`` cube blows
``STACK_MEM_BUDGET_MB``, a single-pass streaming accumulate takes over
(one frame-tile at a time, no stacked cube) - this disables the
cross-frame sigma-clip, a deliberate tradeoff. The reprojected frame set
itself is retained only within an explicit memory budget checked before
allocation; pushing reprojection into the tile loop remains a future
optimization (``reproject_exact`` cost currently makes that impractical).
"""

import logging
import warnings

import numpy as np

from saucepan_pipeline.background import get_background_rms
from saucepan_pipeline.stacking.config import (
    ensure_reprojection_memory_budget,
    resolve_tile_px,
    should_stream,
)
from saucepan_pipeline.stacking.metrics import get_pixel_scale_from_header
from saucepan_pipeline.stacking.models import FrameInfo, StackResult
from saucepan_pipeline.stacking.reproject import (
    reproject_frame,
    reproject_mask,
    reproject_variance,
    select_reference_wcs,
)
from saucepan_pipeline.target_photometry import measure_target_flux

logger = logging.getLogger(__name__)


def _scalar_variance_fallback(frame: FrameInfo) -> float:
    """Positive scalar variance for a frame when no per-pixel value is usable.

    Order: ``background.get_background_rms()`` on the frame's own pixels ->
    scalar ``frame.noise_adu**2`` -> 1.0 (last resort so ``1/var`` is finite).
    """
    try:
        rms = float(get_background_rms(frame.data))
        if np.isfinite(rms) and rms > 0:
            return rms**2
    except Exception:  # noqa: BLE001 - defensive; never abort a stack on this
        pass
    if frame.noise_adu > 0:
        return float(frame.noise_adu) ** 2
    return 1.0


def _resolve_pixel_variance(
    var_map: np.ndarray, valid: np.ndarray, frame: FrameInfo
) -> np.ndarray:
    """Per-pixel variance on the common grid, fully finite and non-negative.

    In-footprint pixels whose reprojected variance is non-finite or
    non-positive are back-filled with :func:`_scalar_variance_fallback`.
    Out-of-footprint pixels are zeroed - their weight is already 0, so the
    ``weight**2 * variance`` term must not carry a NaN into the sum.
    """
    var = np.array(var_map, dtype=np.float64)
    bad_in = valid & (~np.isfinite(var) | (var <= 0.0))
    if bad_in.any():
        var[bad_in] = _scalar_variance_fallback(frame)
    var[~np.isfinite(var)] = 0.0
    return var


def _pct95_proxy(arr: np.ndarray) -> float:
    """Robust per-frame flux proxy: 95th percentile of finite positive pixels.

    Returns 0.0 for a frame with fewer than 100 positive pixels (e.g. a
    near-empty edge frame) so it can't skew the reference median.
    """
    finite = arr[np.isfinite(arr)]
    pos = finite[finite > 0]
    if pos.size < 100:
        return 0.0
    return float(np.percentile(pos, 95))


def _scales_from_proxies(
    target_fluxes: list[float | None] | None,
    pct95s: list[float],
    n: int,
) -> list[float]:
    """Per-frame multiplicative scales from already-reduced proxies.

    Target-anchored when every frame has a valid target flux, else the
    whole-image 95th-percentile fallback. Each scale is ``ref / value_i``
    so the reference frame lands at ~1.
    """
    if target_fluxes is not None and len(target_fluxes) == n:
        valid = [f for f in target_fluxes if f is not None and f > 0]
        if len(valid) == n:
            ref = float(np.median(valid))
            if ref > 0:
                return [ref / f for f in target_fluxes]

    positive = [f for f in pct95s if f > 0]
    if not positive:
        return [1.0] * n
    ref = float(np.median(positive))
    if ref <= 0:
        return [1.0] * n
    return [ref / f if f > 0 else 1.0 for f in pct95s]


def estimate_photometric_scales(
    arrays: list[np.ndarray],
    target_fluxes: list[float | None] | None = None,
) -> list[float]:
    """
    Per-frame multiplicative scales to a common photometric reference (#410).

    Two reference choices, tried in this order:

    1. Target-anchored (#471): if ``target_fluxes`` has one valid (non-None,
       positive) entry per array, scale on that instead. Each flux was
       measured at the science target's own sky position, so it's immune to
       the field-star / cosmic-ray contamination the fallback below is
       vulnerable to - an audited negative-control sweep found the fallback's
       per-frame scale swinging 0.489-1.584 across epochs of the same fixed
       4-node fleet with zero injected signal (#471).
    2. Whole-image fallback (original #410 heuristic, unchanged): median of
       per-frame robust flux proxies (95th percentile of finite positive
       pixels). Background should already be subtracted. Used whenever a
       target flux isn't available for every frame - e.g. calibration frames
       with no SP_RA/SP_DEC, or a target aperture that fell off one frame.

    In both cases each scale is ``ref / flux_i`` so the reference frame gets
    scale ≈ 1.
    """
    pct95s = [_pct95_proxy(arr) for arr in arrays]
    tf = (
        target_fluxes
        if (target_fluxes is not None and len(target_fluxes) == len(arrays))
        else None
    )
    return _scales_from_proxies(tf, pct95s, len(arrays))


def _first_target_radec(frames: list[FrameInfo]) -> tuple[float | None, float | None]:
    """First resolvable (SP_RA, SP_DEC) across the cohort, else ``(None, None)``.

    All frames of one science target carry ~identical SP_RA/SP_DEC, so the
    first is representative.
    """
    for f in frames:
        raw_ra, raw_dec = f.header.get("SP_RA"), f.header.get("SP_DEC")
        if raw_ra is None or raw_dec is None:
            continue
        try:
            return float(raw_ra), float(raw_dec)
        except (TypeError, ValueError):
            continue
    return None, None


def _measure_reprojected_target_fluxes(
    reprojected: list[np.ndarray],
    frames: list[FrameInfo],
    ref_wcs,
) -> list[float | None] | None:
    """Re-measure each frame's target flux in the common (post-reprojection)
    pixel grid, for use as ``estimate_photometric_scales()``'s target-anchored
    reference.

    A frame's own ``target_flux`` (measured in ``_apply_quality()``) is in
    native pixel space, the wrong grid to use here - ``estimate_photometric_scales``
    operates on the already-``reproject_frame()``-resampled arrays. All frames
    of the same science target should carry ~identical SP_RA/SP_DEC, so this
    uses the first one found rather than re-deriving per frame.

    Returns None (meaning: no target to anchor on, caller falls back to the
    whole-image heuristic) if no frame carries a resolvable SP_RA/SP_DEC.
    """
    ra, dec = _first_target_radec(frames)
    if ra is None or dec is None:
        return None

    fluxes: list[float | None] = []
    for arr in reprojected:
        tgt = measure_target_flux(arr, ref_wcs, ra, dec)
        fluxes.append(float(tgt["flux"]) if tgt.get("ok") else None)
    return fluxes


def _iter_tiles(ny: int, nx: int, tile_px: int):
    """Yield ``(y_slice, x_slice)`` tiles covering ``(ny, nx)``."""
    for y0 in range(0, ny, tile_px):
        for x0 in range(0, nx, tile_px):
            yield (
                slice(y0, min(y0 + tile_px, ny)),
                slice(x0, min(x0 + tile_px, nx)),
            )


def _tile_frame_planes(
    sl: tuple,
    reprojected: list[np.ndarray],
    valids: list[np.ndarray],
    variances: list[np.ndarray],
    masks: list[np.ndarray],
    frames: list[FrameInfo],
    weight_by_fwhm: bool,
):
    """Per-frame value / weight / variance planes for one tile.

    Mirrors the pre-clip half of the old whole-frame loop, sliced to the
    tile: inverse-variance * (optional 1/fwhm^2) weight, with uncovered and
    masked (#413) pixels forced to exactly 0.
    """
    ys, xs = sl
    val_planes: list[np.ndarray] = []
    w_planes: list[np.ndarray] = []
    var_planes: list[np.ndarray] = []
    masked_counts = np.zeros(len(frames), dtype=np.int64)

    for i, frame in enumerate(frames):
        valid = valids[i][ys, xs]
        var_pixel = _resolve_pixel_variance(variances[i][ys, xs], valid, frame)

        fwhm_weight = (
            1.0 / (frame.fwhm_arcsec**2)
            if weight_by_fwhm and frame.fwhm_arcsec > 0
            else 1.0
        )

        w_pixel = np.zeros(valid.shape, dtype=np.float64)
        usable = valid & np.isfinite(var_pixel) & (var_pixel > 0.0)
        np.divide(fwhm_weight, var_pixel, out=w_pixel, where=usable)

        mask_i = masks[i][ys, xs] & valid
        masked_counts[i] = int(mask_i.sum())
        if mask_i.any():
            w_pixel[mask_i] = 0.0

        val_planes.append(
            np.where(valid, reprojected[i][ys, xs], 0.0).astype(np.float64)
        )
        w_planes.append(w_pixel)
        var_planes.append(var_pixel)

    return val_planes, w_planes, var_planes, masked_counts


def _combine_tile(
    sl: tuple,
    reprojected: list[np.ndarray],
    valids: list[np.ndarray],
    variances: list[np.ndarray],
    masks: list[np.ndarray],
    frames: list[FrameInfo],
    sigma_clip: float,
    weight_by_fwhm: bool,
    streaming: bool,
    degenerate: np.ndarray | None,
) -> dict:
    """Accumulate one tile's contribution to the stack.

    Returns the tile's ``science``/``weight``/``variance``/``coverage``
    numerators plus the per-frame bookkeeping the caller folds across
    tiles (pre- and post-clip weight totals, clip/mask pixel counts).

    ``degenerate`` (per-frame bool) is ``None`` on the first pass and the
    resolved mask on the second: a degenerate frame's weight is zeroed
    before the numerators are formed, exactly as the old whole-frame code
    did after its global ``surviving_fraction`` check.
    """
    n_frames = len(frames)
    val_planes, w_planes, var_planes, masked_counts = _tile_frame_planes(
        sl, reprojected, valids, variances, masks, frames, weight_by_fwhm
    )

    contributing_counts = np.array(
        [int((w > 0.0).sum()) for w in w_planes], dtype=np.int64
    )
    frame_weight_before = np.array([float(w.sum()) for w in w_planes])

    if streaming:
        # Single-pass online accumulate: one frame-plane at a time, no
        # stacked cube, no cross-frame sigma-clip.
        th = val_planes[0].shape[0] if val_planes else 0
        tw = val_planes[0].shape[1] if val_planes else 0
        science_num = np.zeros((th, tw), dtype=np.float64)
        weight_sum = np.zeros((th, tw), dtype=np.float64)
        variance_num = np.zeros((th, tw), dtype=np.float64)
        n_contributing = np.zeros((th, tw), dtype=np.int32)
        for i in range(n_frames):
            if degenerate is not None and degenerate[i]:
                continue
            w = w_planes[i]
            science_num += val_planes[i] * w
            weight_sum += w
            variance_num += w**2 * var_planes[i]
            n_contributing += (w > 0.0).astype(np.int32)
        return {
            "science_num": science_num,
            "weight_sum": weight_sum,
            "variance_num": variance_num,
            "n_contributing": n_contributing,
            "frame_weight_before": frame_weight_before,
            "frame_weight_after": frame_weight_before.copy(),
            "reject_counts": np.zeros(n_frames, dtype=np.int64),
            "masked_counts": masked_counts,
            "contributing_counts": contributing_counts,
            "clip_iterations": 0,
        }

    val_cube = np.stack(val_planes, axis=0)  # (n_frames, th, tw)
    w_cube = np.stack(w_planes, axis=0)
    var_cube = np.stack(var_planes, axis=0)  # finite & >= 0

    # ------------------------------------------------------------------
    # Iterative per-pixel sigma-clip across the frame axis (#411).
    #
    # Rejection is decided per pixel, not per frame: at each (y, x) every
    # frame's value is compared to the cross-frame robust centre there and
    # the residual is judged against that pixel's own propagated 1-sigma
    # (the #412 per-pixel variance maps; 1.4826*MAD along the frame axis as
    # a fallback wherever the propagated sigma is unusable). Rejected
    # pixels have their weight zeroed. A whole frame is dropped only if its
    # surviving weight fraction (folded across tiles by the caller) falls
    # below ``min_weight_fraction`` - the old "frame 0 never clips" and
    # whole-frame ">50% deviant -> discard" special cases are gone.
    # ------------------------------------------------------------------
    with np.errstate(invalid="ignore"):
        sigma_cube = np.where(
            np.isfinite(var_cube) & (var_cube > 0.0), np.sqrt(var_cube), np.nan
        )

    contributing = w_cube > 0.0  # pixels eligible to contribute, pre-clip

    reject_cube = np.zeros(w_cube.shape, dtype=bool)
    clip_iterations = 0
    # A robust cross-frame median needs at least 3 backers to tell an
    # outlier from the truth, so clipping is a no-op below that.
    if sigma_clip > 0 and n_frames >= 3:
        for iteration in range(5):
            alive = contributing & ~reject_cube
            n_alive = alive.sum(axis=0)
            vals = np.where(alive, val_cube, np.nan)
            with np.errstate(all="ignore"), warnings.catch_warnings():
                warnings.simplefilter("ignore", RuntimeWarning)
                centre = np.nanmedian(vals, axis=0)
                mad = 1.4826 * np.nanmedian(
                    np.abs(vals - centre[None, :, :]), axis=0
                )
            scatter = np.where(np.isfinite(sigma_cube), sigma_cube, mad[None, :, :])
            scatter = np.where(
                np.isfinite(scatter) & (scatter > 0.0), scatter, np.inf
            )
            deviation = np.abs(val_cube - centre[None, :, :])
            new_reject = (
                alive
                & (deviation > sigma_clip * scatter)
                & (n_alive[None, :, :] >= 3)
            )
            updated = reject_cube | new_reject
            clip_iterations = iteration + 1
            if np.array_equal(updated, reject_cube):
                break
            reject_cube = updated

    w_cube[reject_cube] = 0.0
    frame_weight_after = w_cube.sum(axis=(1, 2))
    reject_counts = reject_cube.sum(axis=(1, 2)).astype(np.int64)

    if degenerate is not None:
        for i in range(n_frames):
            if degenerate[i]:
                w_cube[i] = 0.0

    science_num = (val_cube * w_cube).sum(axis=0)
    weight_sum = w_cube.sum(axis=0)
    variance_num = (w_cube**2 * var_cube).sum(axis=0)
    n_contributing = (w_cube > 0.0).sum(axis=0).astype(np.int32)

    return {
        "science_num": science_num,
        "weight_sum": weight_sum,
        "variance_num": variance_num,
        "n_contributing": n_contributing,
        "frame_weight_before": frame_weight_before,
        "frame_weight_after": frame_weight_after,
        "reject_counts": reject_counts,
        "masked_counts": masked_counts,
        "contributing_counts": contributing_counts,
        "clip_iterations": clip_iterations,
    }


def stack_frames(
    frames: list[FrameInfo],
    use_highest_resolution_grid: bool = True,
    sigma_clip: float = 3.0,
    min_weight_fraction: float = 0.02,
    auto_crop: bool = True,
    weight_by_fwhm: bool = True,
    photometric_scale: bool = True,
    tile_px: int | None = None,
    mem_budget_mb: int | None = None,
    force_streaming: bool = False,
) -> StackResult:
    """
    Stack multiple frames into a single science image.

    Process:
    1. Select reference WCS (highest res or centered)
    2. Reproject all frames to common grid
    3. Optional photometric scaling to common flux reference
    4. Compute inverse-variance weights (noise + optional FWHM)
    5. Per-tile: iterative per-pixel sigma-clip across the frame axis, then
       a weighted-mean pass over the post-clip tile weight cube (#414 - the
       accumulator is tiled so peak memory scales with tile area). For very
       large frame counts a single-pass streaming accumulate replaces
       steps 5's cube + clip (``force_streaming`` or an over-budget tile).
    6. Auto-crop to overlap region

    ``tile_px`` / ``mem_budget_mb`` default to ``STACK_TILE_PX`` /
    ``STACK_MEM_BUDGET_MB`` (see ``stacking/config.py``).
    """
    if len(frames) == 0:
        raise ValueError("No frames to stack")

    ref_wcs, target_shape = select_reference_wcs(frames, use_highest_resolution_grid)
    ensure_reprojection_memory_budget(
        len(frames), target_shape[0], target_shape[1], mem_budget_mb
    )
    logger.info(
        'Reference grid: %s, pixel_scale=%s"/px',
        target_shape,
        get_pixel_scale_from_header(frames[0].header) or "unknown",
    )

    reprojected = []
    valids = []
    variances = []
    masks = []  # per-frame exclusion mask on the common grid (#413)
    for frame in frames:
        r, v = reproject_frame(frame, ref_wcs, target_shape)
        # A scalar variance is spatially constant.  Reprojecting a full
        # constant image with reproject_exact only reproduces that scalar and
        # the same footprint already returned by the data reprojection, while
        # allocating another input/output/footprint set.  Keep the exact
        # reproject_variance path for real per-pixel maps, where resampling is
        # required for the science result.
        if getattr(frame, "variance", None) is None:
            scalar_var = float(frame.noise_adu) ** 2 if frame.noise_adu > 0 else 0.0
            var_map = np.full(target_shape, scalar_var, dtype=np.float64)
            var_valid = v
        else:
            var_map, var_valid = reproject_variance(frame, ref_wcs, target_shape)
        reprojected.append(r)
        valids.append(v & var_valid)
        variances.append(var_map)
        masks.append(reproject_mask(frame, ref_wcs, target_shape))

    target_fluxes = (
        _measure_reprojected_target_fluxes(reprojected, frames, ref_wcs)
        if photometric_scale
        else None
    )
    scales = (
        estimate_photometric_scales(reprojected, target_fluxes)
        if photometric_scale
        else [1.0] * len(frames)
    )
    if photometric_scale:
        for i, scale in enumerate(scales):
            if scale != 1.0:
                reprojected[i] = reprojected[i] * scale
            logger.info(
                "Photometric scale %s: %.4f (tele=%s)",
                i,
                scale,
                frames[i].telescope_id,
            )

    # Representative scalar weight per frame - provenance / weight_pct only.
    # The actual combination uses the per-pixel weight maps built per tile.
    for frame in frames:
        if frame.noise_adu > 0:
            base_weight = 1.0 / (frame.noise_adu**2)
        else:
            base_weight = 1.0

        if weight_by_fwhm and frame.fwhm_arcsec > 0:
            fwhm_weight = 1.0 / (frame.fwhm_arcsec**2)
            frame.weight = base_weight * fwhm_weight
        else:
            frame.weight = base_weight

    ny, nx = target_shape
    n_frames = len(frames)

    tpx = resolve_tile_px(tile_px)
    streaming = force_streaming or should_stream(
        n_frames, ny, nx, tile_px, mem_budget_mb
    )
    if streaming:
        logger.info(
            "Streaming stack accumulate (n_frames=%s, tile_px=%s) - "
            "cross-frame sigma-clip disabled",
            n_frames,
            tpx,
        )

    science = np.zeros((ny, nx), dtype=np.float64)
    weight_sum = np.zeros((ny, nx), dtype=np.float64)
    variance_numerator = np.zeros((ny, nx), dtype=np.float64)
    n_contributing = np.zeros((ny, nx), dtype=np.int32)

    frame_weight_before = np.zeros(n_frames, dtype=np.float64)
    frame_weight_after = np.zeros(n_frames, dtype=np.float64)
    reject_px = np.zeros(n_frames, dtype=np.int64)
    masked_px = np.zeros(n_frames, dtype=np.int64)
    contributing_px = np.zeros(n_frames, dtype=np.int64)
    clip_iterations = 0

    tiles = list(_iter_tiles(ny, nx, tpx))

    # Pass 1: clip + accumulate assuming no frame is degenerate; also fold
    # each frame's pre/post-clip weight totals so degeneracy can be judged
    # globally (it is a whole-frame decision, not a per-tile one).
    for sl in tiles:
        tr = _combine_tile(
            sl, reprojected, valids, variances, masks, frames,
            sigma_clip, weight_by_fwhm, streaming, degenerate=None,
        )
        ys, xs = sl
        science[ys, xs] = tr["science_num"]
        weight_sum[ys, xs] = tr["weight_sum"]
        variance_numerator[ys, xs] = tr["variance_num"]
        n_contributing[ys, xs] = tr["n_contributing"]
        frame_weight_before += tr["frame_weight_before"]
        frame_weight_after += tr["frame_weight_after"]
        reject_px += tr["reject_counts"]
        masked_px += tr["masked_counts"]
        contributing_px += tr["contributing_counts"]
        clip_iterations = max(clip_iterations, tr["clip_iterations"])

    with np.errstate(divide="ignore", invalid="ignore"):
        surviving_fraction = np.where(
            frame_weight_before > 0.0,
            frame_weight_after / frame_weight_before,
            1.0,
        )
    degenerate = (frame_weight_before > 0.0) & (
        surviving_fraction < min_weight_fraction
    )

    # Pass 2 (only if a frame degenerated): re-run the identical per-pixel
    # clip and re-form the numerators with the degenerate frames zeroed.
    if degenerate.any():
        for sl in tiles:
            tr = _combine_tile(
                sl, reprojected, valids, variances, masks, frames,
                sigma_clip, weight_by_fwhm, streaming, degenerate=degenerate,
            )
            ys, xs = sl
            science[ys, xs] = tr["science_num"]
            weight_sum[ys, xs] = tr["weight_sum"]
            variance_numerator[ys, xs] = tr["variance_num"]
            n_contributing[ys, xs] = tr["n_contributing"]

    provenance = []
    for i, frame in enumerate(frames):
        fwhm_weight = (
            (1.0 / (frame.fwhm_arcsec**2)) if weight_by_fwhm and frame.fwhm_arcsec > 0 else 1.0
        )
        if degenerate[i]:
            n_rejected_px = int(contributing_px[i])
            reject_reason = "degenerate after per-pixel clip"
        else:
            n_rejected_px = int(reject_px[i])
            reject_reason = f"{n_rejected_px} px clipped" if n_rejected_px else ""

        provenance.append(
            {
                "telescope_id": frame.telescope_id,
                "exptime": frame.exptime,
                "fwhm_arcsec": frame.fwhm_arcsec,
                "pixel_scale": frame.pixel_scale_arcsec,
                "noise_adu": frame.noise_adu,
                "snr": frame.snr,
                "weight": frame.weight,
                "weight_pct": 0.0,
                "fwhm_weight_factor": (
                    round(fwhm_weight, 4) if weight_by_fwhm and frame.fwhm_arcsec > 0 else 1.0
                ),
                "photometric_scale": round(float(scales[i]), 6),
                "rejected": bool(degenerate[i]),
                "reject_reason": reject_reason,
                "n_rejected_pixels": n_rejected_px,
                "clip_iterations": clip_iterations,
                "n_masked_pixels": int(masked_px[i]),
                "mask_sources": list(frame.mask_sources),
            }
        )

    safe_wsum = np.maximum(weight_sum, 1e-10)
    science /= safe_wsum

    max_weight = weight_sum.max() if weight_sum.max() > 0 else 1.0
    low_coverage = weight_sum < (max_weight * min_weight_fraction)
    science[low_coverage] = np.nan

    total_weight = sum(p["weight"] for p in provenance if not p["rejected"])
    if total_weight > 0:
        for p in provenance:
            p["weight_pct"] = round(p["weight"] / total_weight * 100, 1)

    # General weighted-sum error propagation (#467): Var(sum(w_i x_i)/sum(w_i))
    # = sum(w_i^2 var_i) / sum(w_i)^2. This reduces to the simpler
    # sqrt(1/sum(w_i)) only when w_i is exactly the inverse variance -
    # true when weighting by noise alone, false the moment an unrelated
    # factor (here, 1/fwhm_arcsec^2 favoring sharper frames) is folded into
    # the same weight used to combine pixel values. Using the simplified
    # formula with a noise+FWHM weight silently inflated noise_map by the
    # FWHM factor (confirmed via isolation test: toggling weight_by_fwhm
    # alone moved a 2-frame combination's noise_map from 11.4 to 39.6 ADU,
    # when only ~11.4 - noise_adu/sqrt(2) for two equal-noise frames - is
    # physically achievable).
    variance_map = variance_numerator / np.maximum(safe_wsum**2, 1e-20)
    noise_map = np.sqrt(variance_map)
    noise_map[low_coverage] = np.nan

    n_used = sum(1 for p in provenance if not p["rejected"])
    n_rejected_frames = n_frames - n_used

    crop_slice = None
    if auto_crop:
        valid_ys, valid_xs = np.where(~np.isnan(science))
        if len(valid_ys) > 0:
            y1 = max(0, valid_ys.min() - 5)
            y2 = min(ny, valid_ys.max() + 5)
            x1 = max(0, valid_xs.min() - 5)
            x2 = min(nx, valid_xs.max() + 5)
            science = science[y1:y2, x1:x2]
            weight_sum = weight_sum[y1:y2, x1:x2]
            noise_map = noise_map[y1:y2, x1:x2]
            n_contributing = n_contributing[y1:y2, x1:x2]
            ref_wcs = ref_wcs[y1:y2, x1:x2]
            crop_slice = (y1, y2, x1, x2)
            logger.info("Auto-cropped: (%s,%s) → %s", ny, nx, science.shape)

    return StackResult(
        science=science.astype(np.float32),
        weight_map=weight_sum.astype(np.float32),
        noise_map=noise_map.astype(np.float32),
        coverage_map=n_contributing.astype(np.int32),
        ref_wcs=ref_wcs,
        n_frames=n_used,
        n_rejected=n_rejected_frames,
        provenance=provenance,
        crop_slice=crop_slice,
    )
