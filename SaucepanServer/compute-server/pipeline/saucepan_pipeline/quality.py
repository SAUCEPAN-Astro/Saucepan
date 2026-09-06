"""
quality.py — Lambda-compatible image quality assessment.

Fixed SNR measurement: masks stars first, measures noise on background-only pixels.
This fixes the inflated SNR bug in compare_v2.py where MAD was measuring star residuals,
not background noise.

FWHM is measured in-process via DAOStarFinder + Gaussian2D (#409). Client-supplied
SP_FWHM / SEEING headers are never trusted.

CLI:
    python -m pipeline_lambda.quality --input frame.fits

Lambda:
    {"action": "assess_quality", "path": "s3://bucket/frame.fits", ...}
"""

import argparse
import json
import logging

import numpy as np
from astropy.io import fits
from grading.fits_limits import ensure_fits_loadable
from scipy.ndimage import gaussian_filter
from scipy.stats import median_abs_deviation as mad

logger = logging.getLogger(__name__)


def estimate_fwhm(data: np.ndarray, pixel_scale_arcsec: float = 1.0) -> float:
    """
    Measure PSF FWHM from pixels (DAOStarFinder + Gaussian2D fit).

    Returns FWHM in arcsec. Falls back to a gradient proxy if star detection
    or fitting fails. Never reads FITS headers.

    Args:
        data: 2D float image array
        pixel_scale_arcsec: Arcsec per pixel for converting fitted FWHM

    Returns:
        Measured FWHM in arcsec (0.0 if measurement is impossible)
    """
    if data is None or data.size == 0 or pixel_scale_arcsec <= 0:
        return 0.0

    try:
        from astropy.modeling import fitting, models
        from astropy.stats import sigma_clipped_stats
        from photutils.detection import DAOStarFinder

        _mean, median, std = sigma_clipped_stats(data, sigma=3.0)
        if std is None or not np.isfinite(std) or float(std) <= 0:
            return _estimate_fwhm_gradient(data, pixel_scale_arcsec)

        # DAOStarFinder's matched-filter kernel must roughly track the true
        # stellar FWHM in pixels, or it silently misses genuinely sharp
        # point sources - a hardcoded fwhm=5.0 mismatches any telescope
        # with sub-5px native seeing (common: seeing 2-3" at a 0.9-1.2"/px
        # scale is ~2-3px). Confirmed directly (#474): at fwhm=5.0,
        # DAOStarFinder missed an 81778 ADU target entirely (threshold 27)
        # while returning only 12 field-star/noise candidates peaking at
        # 16-34 ADU, none within 34px of the real target - the subsequent
        # per-star Gaussian fits on that junk mostly failed catastrophically
        # (fwhm_px up to 1329), and the one fit that happened to squeak
        # under the sanity ceiling below became the frame's entire reported
        # FWHM. 3.0" is a reasonable generic seeing guess; dividing by
        # pixel_scale_arcsec keeps the kernel matched across telescopes.
        expected_fwhm_px = max(2.0, 3.0 / pixel_scale_arcsec)
        daofind = DAOStarFinder(
            fwhm=expected_fwhm_px, threshold=5.0 * float(std), exclude_border=True
        )
        sources = daofind(data - median)

        if sources is None or len(sources) < 3:
            logger.debug("Too few stars for PSF fit; using gradient FWHM fallback")
            return _estimate_fwhm_gradient(data, pixel_scale_arcsec)

        # Brightest sources first when the table has flux/peak.
        order = np.arange(len(sources))
        for key in ("flux", "peak", "mag"):
            if key in sources.colnames:
                vals = np.asarray(sources[key], dtype=float)
                order = np.argsort(vals)
                if key != "mag":
                    order = order[::-1]
                break

        fwhm_values: list[float] = []
        fit_g = fitting.LevMarLSQFitter()
        xcol = (
            "x_centroid"
            if "x_centroid" in sources.colnames
            else ("xcentroid" if "xcentroid" in sources.colnames else None)
        )
        ycol = (
            "y_centroid"
            if "y_centroid" in sources.colnames
            else ("ycentroid" if "ycentroid" in sources.colnames else None)
        )
        if xcol is None or ycol is None:
            logger.debug("DAOStarFinder table missing centroid columns: %s", sources.colnames)
            return _estimate_fwhm_gradient(data, pixel_scale_arcsec)

        for idx in order[:20]:
            src = sources[int(idx)]
            try:
                x = int(float(src[xcol]))
                y = int(float(src[ycol]))
            except (KeyError, TypeError, ValueError):
                continue
            size = 15
            x0, x1 = max(0, x - size), min(data.shape[1], x + size)
            y0, y1 = max(0, y - size), min(data.shape[0], y + size)
            cutout = data[y0:y1, x0:x1].astype(float)
            if cutout.size < 9:
                continue
            yy, xx = np.mgrid[: cutout.shape[0], : cutout.shape[1]]
            g_init = models.Gaussian2D(
                amplitude=float(np.nanmax(cutout)),
                x_mean=cutout.shape[1] // 2,
                y_mean=cutout.shape[0] // 2,
                x_stddev=3.0,
                y_stddev=3.0,
            )
            try:
                g_fit = fit_g(g_init, xx, yy, cutout)
                sigma_px = (
                    abs(float(g_fit.x_stddev.value)) + abs(float(g_fit.y_stddev.value))
                ) / 2.0
                fwhm_px = sigma_px * 2.355
                if 1.0 < fwhm_px < 30.0:
                    fwhm_values.append(fwhm_px)
            except Exception:
                continue

        # A median of 1-2 values has no robustness against exactly the
        # failure above (one lucky/unlucky survivor of a mostly-failed
        # batch dictating the whole frame's FWHM) - require the same
        # minimum sample size already used for the initial DAOStarFinder
        # detection count check above.
        if len(fwhm_values) < 3:
            logger.debug(
                "Too few successful per-star PSF fits (%d); using gradient FWHM fallback",
                len(fwhm_values),
            )
            return _estimate_fwhm_gradient(data, pixel_scale_arcsec)

        fwhm_pixels = float(np.median(fwhm_values))
        return round(fwhm_pixels * float(pixel_scale_arcsec), 3)

    except ImportError:
        logger.warning("photutils/astropy modeling unavailable; using gradient FWHM estimate")
        return _estimate_fwhm_gradient(data, pixel_scale_arcsec)


def _estimate_fwhm_gradient(data: np.ndarray, pixel_scale_arcsec: float = 1.0) -> float:
    """Fallback gradient-based FWHM estimate in arcsec (less accurate)."""
    smoothed = gaussian_filter(data.astype(float), sigma=1.0)
    gy, gx = np.gradient(smoothed)
    gradient_magnitude = np.sqrt(gy**2 + gx**2)
    # Convert a crude pixel-width proxy; keep positive and finite.
    fwhm_px = float(np.median(gradient_magnitude) * 2.355)
    if not np.isfinite(fwhm_px) or fwhm_px <= 0:
        return 0.0
    # Gradient magnitude is not a true FWHM; clamp to a sane pixel range.
    fwhm_px = float(np.clip(fwhm_px, 1.0, 30.0))
    return round(fwhm_px * float(pixel_scale_arcsec), 3)


def _robust_sigma(values: np.ndarray, center: float) -> float:
    """MAD-equivalent spread estimate, robust to a large population tied at
    a hard floor.

    background.py clips background-subtracted pixels to exactly 0
    (np.clip(x, 0, None)), which for any background-noise-dominated image
    ties close to half of all pixels to that identical value - not a rare
    edge case, a routine consequence of subtracting a median-based estimate
    and clipping the negative half (#464). Computing median()/MAD() over a
    population that's >=50% identical collapses both to 0, which cascades
    into misclassifying every remaining unclipped background pixel as a
    "star" and reporting SNR as exactly zero.

    For symmetric noise, median(|x - center|) over the whole population is
    the same statistic as median(x - center) computed using only the
    values above center - the floor-tied lower half contributes nothing
    but the identical distortion, so restricting to the uncensored upper
    half recovers an unbiased estimate. Divides by the same 0.6745 Gaussian
    scale factor `scale='normal'` already uses elsewhere in this module, so
    this is a restriction of the existing convention, not a new formula.
    """
    upper = values[values > center]
    if len(upper) > 100:
        return float(np.median(upper - center) / 0.6745)
    return float(mad(values, scale="normal"))


def assess_quality(data: np.ndarray, pixel_scale_arcsec: float | None = None) -> dict:
    """
    Assess image quality with proper background-only noise measurement.

    Key fix over compare_v2.py: masks star pixels before measuring noise,
    so MAD measures actual background noise, not residual star flux scatter.

    Args:
        data: 2D float image array
        pixel_scale_arcsec: If set, also measure PSF FWHM in arcsec in-process

    Returns:
        dict with keys:
            background: median pixel value
            noise_adu: MAD of background-only pixels (star-masked)
            signal_adu: adaptive-threshold star-pixel median minus background
            snr: signal_adu / noise_adu
            star_pixels: count of pixels > background + 5*noise
            saturated_pixels: count of pixels >= 65500
            shape: [height, width]
            fwhm_arcsec: measured PSF FWHM (only when pixel_scale_arcsec given)
    """
    flat = data.ravel()
    bg = np.median(flat).item()

    # Step 1: crude noise estimate on all pixels (will be inflated by stars).
    # Floor-robust (#464) - see _robust_sigma().
    crude_noise = _robust_sigma(flat, bg)

    # Step 2: mask star pixels (pixels > bg + 3*crude_noise)
    star_mask = flat > (bg + 3.0 * crude_noise)
    bg_pixels = flat[~star_mask]

    # Step 3: measure noise on background-only pixels. Still floor-robust:
    # bg_pixels is "everything not flagged as a star," which for a
    # background-dominated image is still dominated by the same floor-tied
    # population step 1 had to handle.
    noise_adu = _robust_sigma(bg_pixels, bg) if len(bg_pixels) > 100 else crude_noise

    # Step 4: count star pixels (5-sigma above background)
    star_pixels = int((data > (bg + 5.0 * noise_adu)).sum())

    # Step 5: saturated pixels
    saturated_pixels = int((data >= 65500).sum())

    # Step 6: SNR — median flux of ALL non-saturated star pixels
    # Uses adaptive absolute threshold: try bg+200, fall back to bg+100, bg+50, bg+30
    # This handles both bright real data and fainter synthetic/simulated data.
    for adap_thresh in [200.0, 100.0, 50.0, 30.0]:
        star_pixels_all = flat[(flat > (bg + adap_thresh)) & (flat < 65000)]
        if len(star_pixels_all) > 10:
            signal = np.median(star_pixels_all).item() - bg
            break
    else:
        signal = 0.0
    snr = signal / noise_adu if noise_adu > 0 else 0.0

    result = {
        "background": round(bg, 2),
        "noise_adu": round(noise_adu, 4),
        "signal_adu": round(signal, 4),
        "snr": round(snr, 1),
        "star_pixels": star_pixels,
        "saturated_pixels": saturated_pixels,
        "shape": list(data.shape),
    }
    if pixel_scale_arcsec is not None and float(pixel_scale_arcsec) > 0:
        result["fwhm_arcsec"] = estimate_fwhm(data, float(pixel_scale_arcsec))
    return result


def write_quality_headers(path: str, quality: dict) -> dict:
    """
    Write quality metrics as SP_ keywords into the FITS header.

    Trust model (#292): these headers are write-once by compute (assess),
    not uploader-supplied. Stacking must re-measure from pixels and must not
    treat SP_BGMD / SP_BGNOI / SP_SNR / SP_FWHM on inbound FITS as authoritative.

    Args:
        path: Path to FITS file
        quality: Dict from assess_quality()

    Returns:
        quality dict (passed through for chaining)
    """
    with fits.open(path, mode="update") as hdul:
        hdr = hdul[0].header
        hdr["SP_SNR"] = (quality["snr"], "Signal-to-noise ratio")
        hdr["SP_BGMD"] = (quality["background"], "Background median (ADU)")
        hdr["SP_BGNOI"] = (quality["noise_adu"], "Background noise (ADU)")
        hdr["SP_STARS"] = (quality["star_pixels"], "Star pixel count (5-sigma)")
        hdr["SP_SPX"] = (quality["saturated_pixels"], "Saturated pixel count")
        hdr["SP_NX"] = (quality["shape"][1], "Image width (pixels)")
        hdr["SP_NY"] = (quality["shape"][0], "Image height (pixels)")
        if "fwhm_arcsec" in quality and quality["fwhm_arcsec"] is not None:
            hdr["SP_FWHM"] = (
                float(quality["fwhm_arcsec"]),
                "PSF FWHM arcsec (measured in-process)",
            )
        hdr["SP_PVER"] = ("1.0.0", "Pipeline version")
    return quality


def assess_fits(path: str, update_fits: bool = False) -> dict:
    """
    Load a FITS file and assess its quality.

    Args:
        path: Path to FITS file
        update_fits: If True, write SP_ quality headers into the FITS file

    Returns:
        dict with quality metrics
    """
    with fits.open(path) as hdul:
        ensure_fits_loadable(path, hdul[0].header)
        header = hdul[0].header
        data = hdul[0].data.astype(np.float32)
        pixel_scale = None
        for key in ("SP_PIXSCALE", "PIXSCALE"):
            if key in header:
                pixel_scale = float(header[key])
                break
        if pixel_scale is None and "CDELT2" in header and header["CDELT2"] != 0:
            pixel_scale = abs(float(header["CDELT2"])) * 3600.0
    result = assess_quality(data, pixel_scale_arcsec=pixel_scale or 1.0)
    result["file"] = path

    if update_fits:
        write_quality_headers(path, result)

    return result


# ── Lambda handler ──────────────────────────────────────────────────────


def handler(event: dict, context=None) -> dict:
    """
    AWS Lambda handler for quality assessment.

    Event format:
        {"action": "assess_quality", "path": "/tmp/frame.fits"}
        or
        {"action": "assess_quality", "data_base64": "...", "shape": [h, w]}

    Returns:
        {"statusCode": 200, "body": {...quality dict...}}
    """
    try:
        action = event.get("action", "assess_quality")
        if action != "assess_quality":
            return {"statusCode": 400, "body": {"error": f"Unknown action: {action}"}}

        if "path" in event:
            result = assess_fits(event["path"])
        elif "data_base64" in event:
            import base64

            raw = base64.b64decode(event["data_base64"])
            shape = tuple(event["shape"])
            data = np.frombuffer(raw, dtype=np.float32).reshape(shape)
            result = assess_quality(data)
        else:
            return {"statusCode": 400, "body": {"error": "Need 'path' or 'data_base64'"}}

        return {"statusCode": 200, "body": result}

    except Exception as e:
        logger.exception("Quality assessment failed")
        return {"statusCode": 500, "body": {"error": str(e)}}


# ── CLI entry point ─────────────────────────────────────────────────────


def main():
    parser = argparse.ArgumentParser(description="Assess FITS image quality")
    parser.add_argument("--input", "-i", required=True, help="Input FITS file")
    parser.add_argument("--output", "-o", help="Output JSON (default: stdout)")
    args = parser.parse_args()

    result = assess_fits(args.input)
    output = json.dumps(result, indent=2)

    if args.output:
        with open(args.output, "w") as f:
            f.write(output)
        print(f"Written {args.output}")
    else:
        print(output)


if __name__ == "__main__":
    logging.basicConfig(level=logging.INFO, format="%(levelname)s: %(message)s")
    main()
