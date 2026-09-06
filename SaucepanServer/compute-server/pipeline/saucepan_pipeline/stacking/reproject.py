"""Reproject frames to a common WCS grid and select reference WCS.

Reprojection defaults to ``reproject.reproject_exact`` (flux-conserving:
each output pixel value is the exact sky-area-weighted mean of the input
pixels it overlaps, so total flux is preserved to numerical precision).
``method="interp"`` selects the faster bilinear ``reproject_interp`` for
previews - it is *not* flux-conserving and must not be used for the
science product.

Edge handling: pixels with no input coverage are left as NaN (previously
they were zero-filled by ``nan_to_num``, which injected them into the
stack as real 0.0 signal). Each reprojection returns a companion boolean
``valid`` mask (finite result AND non-trivial footprint); the combiner
uses it to keep uncovered edges out of the weighted sum entirely, rather
than relying on weight arithmetic happening to cancel.

Mask reprojection (``reproject_mask``) carries a frame's native per-pixel
exclusion mask (saturation / bad-pixel map / cosmic rays, #413) onto the
common grid with nearest-neighbour resampling - a mask is categorical, so
no area-weighted blending. The combiner zeroes the weight cube wherever
the reprojected mask is True, before the #411 sigma-clip.

Variance reprojection (``reproject_variance``) resamples a per-pixel
variance map with the *same* method. For ``reproject_exact`` the
pixel-overlap weights conserve variance to first order; the resampled
neighbours are then correlated, which this v1 does not track per-pixel -
see the correlated-noise note in ``combine.py`` (Pont, Zucker & Queloz
2006).
"""


import numpy as np
from astropy.wcs import WCS

from saucepan_pipeline.stacking.models import FrameInfo

_FOOTPRINT_THRESHOLD = 0.5


def _reproject_fn(method: str):
    """Return the underlying reproject callable for ``method``."""
    import reproject as _reproject_pkg

    if method == "exact":
        return _reproject_pkg.reproject_exact
    if method == "interp":
        return _reproject_pkg.reproject_interp
    raise ValueError(f"unknown reproject method: {method!r} (want 'exact' or 'interp')")


def reproject_frame(
    frame: FrameInfo,
    target_wcs: WCS,
    target_shape: tuple,
    method: str = "exact",
) -> tuple:
    """
    Reproject a frame's pixel data to a common WCS grid.

    Args:
        frame: source frame (uses ``frame.data`` and ``frame.wcs``).
        target_wcs: output WCS.
        target_shape: output ``(ny, nx)``.
        method: ``"exact"`` (default, flux-conserving) or ``"interp"``
            (bilinear, faster, for previews only).

    Returns:
        ``(reprojected_data, valid)`` where ``reprojected_data`` is
        float32 with NaN preserved on uncovered pixels, and ``valid`` is a
        boolean array (finite value AND footprint above threshold).
    """
    result, footprint_float = _reproject_fn(method)(
        (frame.data, frame.wcs),
        target_wcs,
        shape_out=target_shape,
    )

    result = np.asarray(result, dtype=np.float32)
    if isinstance(footprint_float, np.ndarray):
        covered = footprint_float > _FOOTPRINT_THRESHOLD
    else:
        covered = np.ones(target_shape, dtype=bool)

    valid = np.isfinite(result) & covered
    return result, valid


def reproject_mask(
    frame: FrameInfo,
    target_wcs: WCS,
    target_shape: tuple,
) -> np.ndarray:
    """Carry a frame's native per-pixel exclusion mask onto the common grid.

    Nearest-neighbour resampling (``reproject_interp`` ``order=0``) - a mask
    is categorical, so bilinear/area-weighted resampling would smear a hard
    edge into fractional values. An input pixel flagged True lands on every
    output pixel whose centre falls inside it.

    Args:
        frame: source frame; uses ``frame.mask`` (bool, True = exclude) and
            ``frame.wcs``. A None mask means nothing is flagged.
        target_wcs: output WCS.
        target_shape: output ``(ny, nx)``.

    Returns:
        Bool array of ``target_shape``, True where a masked input pixel
        reprojects. All-False when ``frame.mask`` is None. Uncovered output
        pixels are False here - the combiner's ``valid`` mask already keeps
        those out of the stack.
    """
    if frame.mask is None:
        return np.zeros(target_shape, dtype=bool)

    from reproject import reproject_interp

    src = np.asarray(frame.mask, dtype=np.float32)
    result, _footprint = reproject_interp(
        (src, frame.wcs),
        target_wcs,
        shape_out=target_shape,
        order=0,
    )
    result = np.nan_to_num(np.asarray(result), nan=0.0)
    return result > 0.5


def reproject_variance(
    frame: FrameInfo,
    target_wcs: WCS,
    target_shape: tuple,
    method: str = "exact",
) -> tuple:
    """
    Reproject a per-pixel variance map onto the common grid.

    Uses ``frame.variance`` when present, otherwise broadcasts the scalar
    ``frame.noise_adu**2`` (no per-pixel map available yet). Resampling
    uses the *same* ``method`` as :func:`reproject_frame`; for
    ``reproject_exact`` the sky-area overlap weights conserve variance to
    first order. Correlated noise between resampled neighbours is not
    tracked here (see ``combine.py``).

    Returns:
        ``(reprojected_variance, valid)`` - float64, NaN preserved on
        uncovered pixels, plus the boolean coverage mask.
    """
    src_var = getattr(frame, "variance", None)
    if src_var is None:
        scalar_var = float(frame.noise_adu) ** 2 if frame.noise_adu > 0 else 0.0
        src_var = np.full(frame.data.shape, scalar_var, dtype=np.float64)
    else:
        src_var = np.asarray(src_var, dtype=np.float64)

    result, footprint_float = _reproject_fn(method)(
        (src_var, frame.wcs),
        target_wcs,
        shape_out=target_shape,
    )

    result = np.asarray(result, dtype=np.float64)
    if isinstance(footprint_float, np.ndarray):
        covered = footprint_float > _FOOTPRINT_THRESHOLD
    else:
        covered = np.ones(target_shape, dtype=bool)

    valid = np.isfinite(result) & covered
    return result, valid


def select_reference_wcs(
    frames: list[FrameInfo],
    use_highest_resolution: bool = True,
) -> tuple:
    """
    Select the reference (output) WCS for stacking.

    Option A (default): highest-resolution telescope WCS.
    Option B: median pixel scale and center of all frames.

    Returns:
        (wcs, (height, width))
    """
    if use_highest_resolution and len(frames) > 0:
        valid_ps = [(i, f) for i, f in enumerate(frames) if f.pixel_scale_arcsec > 0]
        if valid_ps:
            best = min(valid_ps, key=lambda x: x[1].pixel_scale_arcsec)[1]
            h, w = best.data.shape
            return best.wcs, (h, w)

    pixel_scales = [f.pixel_scale_arcsec for f in frames if f.pixel_scale_arcsec > 0]
    target_ps = np.median(pixel_scales) if pixel_scales else 1.0

    ras = []
    decs = []
    for f in frames:
        try:
            ra = float(f.header.get("SP_RA") or f.header.get("CRVAL1") or 0.0)
            dec = float(f.header.get("SP_DEC") or f.header.get("CRVAL2") or 0.0)
            if ra != 0.0 or dec != 0.0:
                ras.append(ra)
                decs.append(dec)
        except (TypeError, ValueError):
            pass

    if not ras:
        ref = frames[0]
        ny, nx = ref.data.shape
        return ref.wcs, (ny, nx)

    ra_center = np.mean(ras)
    dec_center = np.mean(decs)
    scale_deg = target_ps / 3600.0

    max_h, max_w = 0, 0
    for f in frames:
        h, w = f.data.shape
        if f.pixel_scale_arcsec > 0 and target_ps > 0:
            ratio = f.pixel_scale_arcsec / target_ps
            h = int(h * ratio)
            w = int(w * ratio)
        max_h = max(max_h, h)
        max_w = max(max_w, w)

    ny = max(512, int(max_h * 1.1))
    nx = max(512, int(max_w * 1.1))

    wcs = WCS(naxis=2)
    wcs.wcs.crpix = [nx / 2.0, ny / 2.0]
    wcs.wcs.cdelt = [-scale_deg, scale_deg]
    wcs.wcs.crval = [ra_center, dec_center]
    wcs.wcs.ctype = ["RA---TAN", "DEC--TAN"]

    return wcs, (ny, nx)
