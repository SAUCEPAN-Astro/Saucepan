"""
psf_match.py — PSF matching for heterogeneous telescope data.

Convolves each frame to a common target PSF so frames from different
telescopes (different optics/seeing) can be scientifically combined.

Must run BEFORE reprojection (in native pixel space).
Science stacking for heterogeneous frames (canonical: saucepan_pipeline).
"""

import logging
import statistics

import numpy as np

logger = logging.getLogger(__name__)


MATCH_METHODS = ("gaussian-fft", "alard-lupton")


def match_psf(
    data: np.ndarray,
    source_fwhm_arcsec: float,
    target_fwhm_arcsec: float,
    pixel_scale_arcsec: float,
    method: str = "gaussian-fft",
) -> np.ndarray:
    """
    Convolve data to broaden PSF from source_fwhm to target_fwhm.

    Args:
        data: 2D image array (float32)
        source_fwhm_arcsec: Current frame FWHM in arcsec
        target_fwhm_arcsec: Target (worst accepted) FWHM in arcsec
        pixel_scale_arcsec: Pixel scale in arcsec/pixel
        method: ``"gaussian-fft"`` (default, unchanged) fits a single isotropic
            Gaussian kernel of sigma ``sqrt(sigma_T^2 - sigma_S^2)``.
            ``"alard-lupton"`` fits a basis-of-Gaussians x polynomial kernel
            (Alard & Lupton 1998) against circular Gaussian model PSFs built
            from the two FWHMs — see ``psf_match_al``. Both run in native pixel
            space, before reprojection.

    Returns:
        PSF-matched image array (same shape as input, float32)

    Raises:
        ValueError: if source_fwhm >= target_fwhm (cannot sharpen PSF), or if
            ``method`` is not one of ``MATCH_METHODS``.
    """
    if source_fwhm_arcsec >= target_fwhm_arcsec:
        raise ValueError(
            f'Cannot sharpen PSF: source FWHM ({source_fwhm_arcsec:.2f}") '
            f'>= target FWHM ({target_fwhm_arcsec:.2f}")'
        )
    if method not in MATCH_METHODS:
        raise ValueError(
            f"Unknown PSF match method: {method!r} (expected one of {MATCH_METHODS})"
        )

    if method == "alard-lupton":
        matched, _info = _match_psf_alard_lupton(
            data, source_fwhm_arcsec, target_fwhm_arcsec, pixel_scale_arcsec
        )
        return matched

    # Convert to pixels
    source_fwhm_px = source_fwhm_arcsec / pixel_scale_arcsec
    target_fwhm_px = target_fwhm_arcsec / pixel_scale_arcsec

    fwhm_to_sigma = 1.0 / 2.3548
    source_sigma = source_fwhm_px * fwhm_to_sigma
    target_sigma = target_fwhm_px * fwhm_to_sigma

    # Kernel sigma: sqrt(target² - source²)
    kernel_sigma = np.sqrt(target_sigma**2 - source_sigma**2)

    logger.info(
        f'PSF matching: {source_fwhm_arcsec:.2f}" → {target_fwhm_arcsec:.2f}" '
        f"(kernel σ={kernel_sigma:.2f} px)"
    )

    from astropy.convolution import Gaussian2DKernel, convolve_fft

    kernel = Gaussian2DKernel(x_stddev=kernel_sigma)
    result = convolve_fft(data, kernel, allow_huge=True)

    return result.astype(np.float32)


def _match_psf_alard_lupton(
    data: np.ndarray,
    source_fwhm_arcsec: float,
    target_fwhm_arcsec: float,
    pixel_scale_arcsec: float,
) -> tuple[np.ndarray, dict]:
    """Route the scalar-FWHM call to the A&L kernel-matching backend.

    Builds circular Gaussian model PSFs from the two FWHMs and fits a
    basis-of-Gaussians x polynomial kernel (``psf_match_al.match_psf_al``).
    For round PSFs this reproduces the ``gaussian-fft`` broadening to within
    a fraction of a percent; its reason to exist is elongated PSFs, where the
    fitted kernel can be anisotropic. Returns ``(matched_float32, info)``.

    Raises:
        ValueError: if source_fwhm >= target_fwhm (cannot sharpen PSF).
    """
    if source_fwhm_arcsec >= target_fwhm_arcsec:
        raise ValueError(
            f'Cannot sharpen PSF: source FWHM ({source_fwhm_arcsec:.2f}") '
            f'>= target FWHM ({target_fwhm_arcsec:.2f}")'
        )
    from saucepan_pipeline.psf_match_al import (
        FWHM_TO_SIGMA,
        gaussian_psf_stamp,
        match_psf_al,
    )

    source_sigma_px = (source_fwhm_arcsec / pixel_scale_arcsec) * FWHM_TO_SIGMA
    target_sigma_px = (target_fwhm_arcsec / pixel_scale_arcsec) * FWHM_TO_SIGMA

    # Stamp large enough for the target PSF and the default kernel footprint.
    stamp = 2 * int(round(6.0 * target_sigma_px)) + 1
    stamp = max(stamp, 25)
    src_psf = gaussian_psf_stamp(source_sigma_px, stamp)
    tgt_psf = gaussian_psf_stamp(target_sigma_px, stamp)

    logger.info(
        'PSF matching (alard-lupton): %.2f" -> %.2f" (source sigma=%.2f px, '
        "target sigma=%.2f px)",
        source_fwhm_arcsec,
        target_fwhm_arcsec,
        source_sigma_px,
        target_sigma_px,
    )

    matched, info = match_psf_al(data, src_psf, tgt_psf)
    return matched.astype(np.float32), info


def select_target_psf(fwhm_list: list) -> float:
    """
    Given per-frame FWHM values (arcsec), return the epoch's PSF-match target.

    The intent is "resolve to the worst *real* seeing among accepted frames".
    A plain ``max()`` has no defense against a single frame whose FWHM was
    mis-measured high (#474/#480): one such outlier drags the whole epoch's
    target up and forces every other frame to over-convolve and lose flux.

    So for 4+ frames we reject a lone upward outlier before taking the max:
    a value is kept only if it sits at or below a robust upper fence
    ``median + 3.5 * 1.4826 * MAD`` of the fleet. Two frames above the fence
    corroborate each other and count as genuine bad seeing; a single one is
    dropped and the target falls back to the next-worst frame. With fewer
    than 4 frames there is no peer group to judge an outlier against, so the
    original ``max()`` behaviour is kept.
    """
    vals = sorted(float(x) for x in fwhm_list)
    if not vals:
        raise ValueError("select_target_psf: empty FWHM list")
    if len(vals) < 4:
        return vals[-1]

    med = statistics.median(vals)
    mad = statistics.median([abs(v - med) for v in vals])
    if mad == 0.0:
        # degenerate spread (e.g. near-identical FWHMs) — nothing to reject.
        return vals[-1]

    fence = med + 3.5 * 1.4826 * mad
    above = [v for v in vals if v > fence]
    if len(above) != 1:
        # 0 above → nothing to reject; 2+ above → they corroborate each other
        # as genuine bad seeing. Either way, resolve to the worst frame.
        return vals[-1]

    # exactly one frame is far above the rest of the fleet — almost certainly
    # a mis-measurement, not real seeing. Resolve to the next-worst frame.
    logger.warning(
        'select_target_psf: dropping FWHM outlier %.3f" (fleet median %.3f", '
        'fence %.3f"), resolving to %.3f"',
        vals[-1],
        med,
        fence,
        vals[-2],
    )
    return vals[-2]


def write_psf_headers(
    header,
    source_fwhm: float,
    target_fwhm: float,
    kernel_sigma_px: float,
    *,
    method: str = "gaussian-fft",
    kernel_size: int | None = None,
    basis_sigmas_px: tuple[float, ...] | None = None,
) -> None:
    """Write PSF matching metadata as SP_ keywords into a FITS header.

    Args:
        header: FITS header to mutate.
        source_fwhm: Input PSF FWHM (arcsec).
        target_fwhm: Target PSF FWHM (arcsec).
        kernel_sigma_px: Equivalent isotropic kernel sigma (pixels) — for
            ``alard-lupton`` this is the ``sqrt(sigma_T^2 - sigma_S^2)``
            reference value, not the fitted kernel (which is not a single sigma).
        method: The method actually used (``"gaussian-fft"`` or
            ``"alard-lupton"``); recorded verbatim in ``SP_PSF_METH``.
        kernel_size: A&L kernel side length in pixels — written as
            ``SP_PSF_KSZ`` when given.
        basis_sigmas_px: A&L basis Gaussian widths in pixels — written as
            ``SP_PSF_BW`` (comma-joined) when given.
    """
    header["SP_PSF_MATCH"] = (True, "Was PSF matched before stacking?")
    header["SP_PSF_IN"] = (round(source_fwhm, 3), "Input PSF FWHM (arcsec)")
    header["SP_PSF_OUT"] = (round(target_fwhm, 3), "Target PSF FWHM (arcsec)")
    header["SP_PSF_KSIG"] = (round(kernel_sigma_px, 3), "PSF kernel sigma (pixels)")
    header["SP_PSF_METH"] = (method, "PSF matching method")
    if kernel_size is not None:
        header["SP_PSF_KSZ"] = (int(kernel_size), "A&L matching kernel size (pixels)")
    if basis_sigmas_px is not None:
        header["SP_PSF_BW"] = (
            ",".join(f"{float(s):g}" for s in basis_sigmas_px),
            "A&L basis Gaussian sigmas (pixels)",
        )


def handler(event: dict, context=None) -> dict:
    """
    Lambda handler for PSF matching.

    Event:
        {"action": "match_psf", "data_base64": "...", "shape": [h, w],
         "source_fwhm": 2.0, "target_fwhm": 3.5, "pixel_scale": 1.2,
         "method": "gaussian-fft"}   # method optional, defaults to gaussian-fft
    """
    import base64

    action = event.get("action")
    if action != "match_psf":
        return {"statusCode": 400, "body": {"error": f"Unknown action: {action}"}}

    try:
        raw = base64.b64decode(event["data_base64"])
        data = np.frombuffer(raw, dtype=np.float32).reshape(tuple(event["shape"]))

        source_fwhm = float(event["source_fwhm"])
        target_fwhm = float(event["target_fwhm"])
        pixel_scale = float(event["pixel_scale"])
        method = event.get("method", "gaussian-fft")

        if method == "alard-lupton":
            result, al_info = _match_psf_alard_lupton(
                data, source_fwhm, target_fwhm, pixel_scale
            )
        else:
            result = match_psf(data, source_fwhm, target_fwhm, pixel_scale, method=method)
            al_info = None

        # Reference isotropic kernel sigma (also the gaussian-fft kernel). Only
        # meaningful once source < target; match_psf above rejects otherwise.
        source_fwhm_px = source_fwhm / pixel_scale
        target_fwhm_px = target_fwhm / pixel_scale
        kernel_sigma_px = float(
            np.sqrt((target_fwhm_px / 2.3548) ** 2 - (source_fwhm_px / 2.3548) ** 2)
        )

        metadata = {
            "psf_matched": True,
            "method": method,
            "source_fwhm": source_fwhm,
            "target_fwhm": target_fwhm,
            "kernel_sigma_px": round(kernel_sigma_px, 3),
        }
        if al_info is not None:
            metadata["kernel_size"] = al_info["kernel_size"]
            metadata["basis_sigmas_px"] = list(al_info["basis_sigmas_px"])
            metadata["variance_inflation"] = round(al_info["variance_inflation"], 4)
            metadata["kernel_neg_fraction"] = round(al_info["kernel_neg_fraction"], 4)

        return {
            "statusCode": 200,
            "body": {
                "data_base64": base64.b64encode(result.tobytes()).decode(),
                "shape": list(result.shape),
                "metadata": metadata,
            },
        }
    except Exception as e:
        logger.exception("PSF matching failed")
        return {"statusCode": 500, "body": {"error": str(e)}}
