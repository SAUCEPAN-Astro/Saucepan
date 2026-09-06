"""
reproject.py — WCS reprojection for heterogeneous telescope data.

Handles different pixel scales from different telescopes.
Must run AFTER PSF matching (which works in native pixel space).

Science stacking for heterogeneous frames (canonical: saucepan_pipeline).
"""

import logging

import numpy as np
from astropy.io import fits
from astropy.wcs import WCS

logger = logging.getLogger(__name__)


def get_pixel_scale(header: fits.Header) -> float | None:
    """Extract pixel scale in arcsec/pixel from FITS header."""
    for key in ["SP_PIXSCALE", "PIXSCALE"]:
        if key in header:
            return float(header[key])
    if "CDELT2" in header:
        cdelt = header["CDELT2"]
        if cdelt != 0:
            return abs(float(cdelt)) * 3600.0
    if "CD2_2" in header:
        cd = header["CD2_2"]
        if cd != 0:
            return abs(float(cd)) * 3600.0
    return None


def get_pixel_scale_from_wcs(wcs: WCS) -> float:
    """Extract pixel scale from a WCS object."""
    try:
        cdelt = wcs.wcs.cdelt
        if cdelt is not None and len(cdelt) >= 2 and cdelt[1] != 0:
            return abs(float(cdelt[1])) * 3600.0
    except Exception:
        pass
    return 0.0


def extract_wcs(header: fits.Header, data_shape: tuple) -> WCS:
    """
    Extract or build a WCS from a FITS header.

    Tries WCS(header) first; if that fails, builds minimal TAN WCS
    from SP_RA/SP_DEC/SP_PIXSCALE or fallback keywords.

    Args:
        header: FITS header
        data_shape: (height, width) for fallback WCS

    Returns:
        Astropy WCS object
    """
    try:
        wcs = WCS(header)
        if wcs.has_celestial and wcs.naxis > 0:
            return wcs
    except Exception:
        pass

    # Fallback: build minimal WCS
    ra = header.get("SP_RA") or header.get("CRVAL1") or header.get("RA", 0.0)
    dec = header.get("SP_DEC") or header.get("CRVAL2") or header.get("DEC", 0.0)
    ps = get_pixel_scale(header) or 1.0
    h, w = data_shape

    wcs_header = fits.Header()
    wcs_header["NAXIS"] = 2
    wcs_header["NAXIS1"] = w
    wcs_header["NAXIS2"] = h
    wcs_header["CTYPE1"] = "RA---TAN"
    wcs_header["CTYPE2"] = "DEC--TAN"
    wcs_header["CRPIX1"] = w / 2.0
    wcs_header["CRPIX2"] = h / 2.0
    wcs_header["CRVAL1"] = float(ra)
    wcs_header["CRVAL2"] = float(dec)
    wcs_header["CDELT1"] = -ps / 3600.0
    wcs_header["CDELT2"] = ps / 3600.0

    logger.warning(f'Built fallback WCS from header keywords: RA={ra}, Dec={dec}, PS={ps}"/px')
    return WCS(wcs_header)


def write_reproject_headers(
    header: fits.Header,
    in_pixel_scale: float,
    out_pixel_scale: float,
    method: str = "interp",
) -> None:
    """Write reprojection metadata as SP_ keywords into a FITS header."""
    header["SP_REPROJ"] = (True, "Was reprojected to common WCS?")
    header["SP_REPMETH"] = (method, "Reprojection method")
    header["SP_IN_PX"] = (round(in_pixel_scale, 4), "Input pixel scale (arcsec/px)")
    header["SP_OUT_PX"] = (round(out_pixel_scale, 4), "Output pixel scale (arcsec/px)")
    px_loss = round(in_pixel_scale / out_pixel_scale, 4) if out_pixel_scale > 0 else 1.0
    header["SP_PX_RET"] = (min(px_loss, 1.0), "Information retention factor (1.0 = none lost)")


def reproject_frame(
    data: np.ndarray,
    source_header: fits.Header,
    target_wcs: WCS,
    target_shape: tuple,
) -> tuple:
    """
    Reproject data from its native WCS to a common output WCS.

    Args:
        data: 2D float32 image array
        source_header: FITS header with WCS keywords
        target_wcs: Output WCS
        target_shape: (height, width)

    Returns:
        (reprojected_data, footprint_bool)
    """
    from reproject import reproject_interp

    # Build source WCS
    source_wcs = extract_wcs(source_header, data.shape)

    try:
        result, footprint_float = reproject_interp(
            (data, source_wcs),
            target_wcs,
            shape_out=target_shape,
        )

        # Safe footprint conversion
        if isinstance(footprint_float, np.ndarray):
            footprint = footprint_float > 0.5
        else:
            footprint = np.ones(target_shape, dtype=bool)

        result = np.nan_to_num(result, nan=0.0).astype(np.float32)

        coverage = (footprint.sum() / footprint.size) * 100
        logger.info(f"Reprojected: {data.shape} → {target_shape}, coverage={coverage:.1f}%")

        # Write reprojection metadata into source header (in-place)
        in_ps = get_pixel_scale(source_header) or 0.0
        out_ps = get_pixel_scale_from_wcs(target_wcs)
        write_reproject_headers(source_header, in_ps, out_ps)

        return result, footprint

    except ImportError:
        logger.warning("reproject package not available; using fallback")
        result = np.zeros(target_shape, dtype=np.float32)
        h = min(data.shape[0], target_shape[0])
        w = min(data.shape[1], target_shape[1])
        result[:h, :w] = data[:h, :w]
        footprint = np.zeros(target_shape, dtype=bool)
        footprint[:h, :w] = True
        return result, footprint


# ── Lambda handler ──────────────────────────────────────────────────────


def handler(event: dict, context=None) -> dict:
    """
    Lambda handler for reprojection.

    Event:
        {"action": "reproject", "data_base64": "...", "shape": [h, w],
         "header": {...SP_ / WCS keywords...},
         "target_wcs_json": {...WCS params...}, "target_shape": [H, W]}
    """
    import base64

    action = event.get("action")
    if action != "reproject":
        return {"statusCode": 400, "body": {"error": f"Unknown action: {action}"}}

    try:
        raw = base64.b64decode(event["data_base64"])
        data = np.frombuffer(raw, dtype=np.float32).reshape(tuple(event["shape"]))

        # Rebuild source header
        source_header = fits.Header()
        source_header.update(event["header"])

        # Rebuild target WCS
        tw = event["target_wcs_json"]
        target_wcs = WCS(naxis=2)
        target_wcs.wcs.crpix = tw.get("crpix", [0.0, 0.0])
        target_wcs.wcs.crval = tw.get("crval", [0.0, 0.0])
        target_wcs.wcs.cdelt = tw.get("cdelt", [1.0, 1.0])
        target_wcs.wcs.ctype = tw.get("ctype", ["RA---TAN", "DEC--TAN"])
        target_shape = tuple(event["target_shape"])

        result, footprint = reproject_frame(data, source_header, target_wcs, target_shape)

        return {
            "statusCode": 200,
            "body": {
                "data_base64": base64.b64encode(result.tobytes()).decode(),
                "shape": list(result.shape),
                "footprint_shape": list(footprint.shape),
            },
        }
    except Exception as e:
        logger.exception("Reprojection failed")
        return {"statusCode": 500, "body": {"error": str(e)}}
