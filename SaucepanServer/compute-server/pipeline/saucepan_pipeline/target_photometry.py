"""Position-anchored aperture photometry at a known sky position (#471).

quality.py::assess_quality() and stacking/combine.py::estimate_photometric_scales()
both measure signal from whole-image heuristics (adaptive-threshold pixel
median, 95th-percentile flux) that have no notion of *which* pixel is the
actual science target - they pick up whatever crosses a brightness threshold,
which conflates the target with field stars / cosmic-ray residuals. Every
frame already carries SP_RA/SP_DEC (Tier-1 mandatory headers) and a per-frame
WCS, so the target's sky position is always available - this module measures
flux there directly instead.

This is a reimplementation, not an import, of the pattern already audited in
``validation/injection_recovery/reference.py::measure_flux_at_sky_position()``.
Validation code must stay independent of the production pipeline it checks
(see ``validation/injection_recovery/tests/test_import_boundary.py``), and the
reverse direction - production importing validation/test code - is
architecturally backwards, so the ~30-line aperture+annulus math is
reimplemented here as a small, pipeline-owned unit operating on an in-memory
array + WCS object (the pipeline already has both in memory mid-stage; no
need to round-trip through a FITS path the way the file-based validation
reference does).
"""

from __future__ import annotations

import numpy as np
from astropy.wcs import WCS


def measure_target_flux(
    data: np.ndarray,
    wcs: WCS,
    ra_deg: float,
    dec_deg: float,
    *,
    aperture_radius_px: float = 12.0,
    annulus_inner_px: float = 18.0,
    annulus_outer_px: float = 26.0,
) -> dict:
    """Sum-in-aperture flux at a known sky position, with annulus background
    subtraction. Same minimal, auditable pattern as
    ``reference.py::measure_flux_at_sky_position`` - deliberately not
    photutils-dependent, not a second pipeline.

    Args:
        data: 2D float image array (already background-subtracted, in
            whatever pixel space ``wcs`` describes - native frame space or a
            common reprojected grid both work, since this function only
            needs the array/WCS pair to be self-consistent).
        wcs: WCS describing ``data``'s pixel grid.
        ra_deg: Target right ascension, degrees.
        dec_deg: Target declination, degrees.
        aperture_radius_px: Source aperture radius in pixels.
        annulus_inner_px: Background annulus inner radius in pixels.
        annulus_outer_px: Background annulus outer radius in pixels.

    Returns:
        dict with ``"ok"`` (bool). When True: ``"flux"``, ``"x"``, ``"y"``,
        ``"raw_aperture_sum"``, ``"bg_per_px"``, ``"n_aperture_px"``. When
        False: ``"reason"``, ``"x"``, ``"y"``.
    """
    try:
        x, y = wcs.all_world2pix(ra_deg, dec_deg, 0)
        x, y = float(x), float(y)
    except Exception as exc:  # noqa: BLE001 - report, don't hide
        return {"ok": False, "reason": f"wcs projection failed: {exc}", "x": None, "y": None}

    if not (np.isfinite(x) and np.isfinite(y)):
        return {"ok": False, "reason": "non-finite pixel position", "x": x, "y": y}

    h, w = data.shape

    # The masks can only select pixels within the largest requested radius.
    # Keep the original full-frame path for non-finite radii: besides avoiding
    # surprising changes for invalid inputs, this preserves NumPy's behavior
    # for all public parameter values that cannot define a finite box.
    radii = (aperture_radius_px, annulus_outer_px)
    use_bounded_grid = all(np.isfinite(radius) for radius in radii)
    if use_bounded_grid:
        radius = max(0.0, *radii)
        x0 = max(0, int(np.ceil(x - radius)))
        x1 = max(0, min(w, int(np.floor(x + radius)) + 1))
        y0 = max(0, int(np.ceil(y - radius)))
        y1 = max(0, min(h, int(np.floor(y + radius)) + 1))
        if x0 >= x1 or y0 >= y1:
            return {"ok": False, "reason": "aperture off frame", "x": x, "y": y}
        yy, xx = np.mgrid[y0:y1, x0:x1]
    else:
        yy, xx = np.mgrid[0:h, 0:w]
    r = np.sqrt((xx - x) ** 2 + (yy - y) ** 2)

    aperture_mask = r <= aperture_radius_px
    annulus_mask = (r >= annulus_inner_px) & (r <= annulus_outer_px)

    if not np.any(aperture_mask):
        return {"ok": False, "reason": "aperture off frame", "x": x, "y": y}

    bounded_data = data[y0:y1, x0:x1] if use_bounded_grid else data
    aperture_sum = float(np.nansum(bounded_data[aperture_mask]))
    n_ap_px = int(np.sum(aperture_mask))

    if np.any(annulus_mask):
        bg_per_px = float(np.nanmedian(bounded_data[annulus_mask]))
    else:
        bg_per_px = 0.0

    flux = aperture_sum - bg_per_px * n_ap_px
    return {
        "ok": True,
        "x": x,
        "y": y,
        "raw_aperture_sum": aperture_sum,
        "bg_per_px": bg_per_px,
        "n_aperture_px": n_ap_px,
        "flux": flux,
    }
