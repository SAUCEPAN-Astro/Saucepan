"""
psf_match_al.py — Alard & Lupton (1998) basis-of-Gaussians kernel matching.

Optional PSF-matching backend, reached only when a caller explicitly asks for
``psf_match.match_psf(..., method="alard-lupton")``. The default pipeline path
stays ``"gaussian-fft"`` and is unchanged.

Method
------
Alard & Lupton 1998 ("A Method for Optimal Image Subtraction", ApJ 503, 325):
the kernel ``K`` that convolves a better-seeing image toward a target PSF is
expanded on a *fixed* basis of Gaussians modulated by 2-D polynomials,

    K(u, v) = sum_n exp(-(u^2 + v^2) / (2 sigma_n^2)) * sum_{p+q <= d_n} a_npq u^p v^q

and the coefficients ``a_npq`` are the linear least-squares solution of

    source_psf (*) K  ~=  target_psf            ((*) = 2-D convolution).

The polynomial factors let each circular basis Gaussian take an elliptical or
skewed effective shape, so the fitted kernel can correct PSF *elongation* — a
single isotropic Gaussian kernel (the ``gaussian-fft`` path) cannot, because
``sqrt(sigma_T^2 - sigma_S^2)`` is one scalar and blurs every axis equally.

v1 scope
--------
A single **global** kernel — no spatial variation across the frame. This is
adequate while the Saucepan per-frame model treats the PSF as field-constant.
A&L's full spatially varying form (coefficients themselves low-order
polynomials in image position) is a future upgrade and is not implemented.

Noise caveat
------------
Convolving by ``K`` correlates pixel noise, and negative lobes in ``K`` inflate
the output variance: for white input noise the per-pixel variance is scaled by
``sum(K_i^2)`` (vs ``sum(K_i)^2`` for the equivalent flux-conserving positive
kernel). Downstream inverse-variance weights (#298) must use the post-match
noise, not the pre-match value. See the white paper Section 6.4.
"""

from __future__ import annotations

import numpy as np

FWHM_TO_SIGMA = 1.0 / 2.3548

# Basis Gaussian widths (pixels) and the per-Gaussian polynomial degree
# (all monomials u^p v^q with p + q <= degree). Wider Gaussians carry lower
# polynomial order — the A&L convention — so the design matrix stays modest
# (4->15, 2->6, 0->1 = 22 columns) and well conditioned.
DEFAULT_BASIS_SIGMAS_PX: tuple[float, ...] = (1.0, 2.0, 3.0)
DEFAULT_DEGREES: tuple[int, ...] = (4, 2, 0)


def gaussian_psf_stamp(sigma_px: float, size: int) -> np.ndarray:
    """Return a unit-sum, centred, circular Gaussian PSF on a square odd grid.

    Args:
        sigma_px: Gaussian sigma in pixels.
        size: Stamp side length; forced odd so the PSF is pixel-centred.

    Returns:
        ``(size, size)`` float64 array summing to 1.0.
    """
    size = int(size) | 1
    half = size // 2
    y, x = np.mgrid[-half : half + 1, -half : half + 1]
    psf = np.exp(-(x**2 + y**2) / (2.0 * float(sigma_px) ** 2))
    return (psf / psf.sum()).astype(np.float64)


def elliptical_gaussian_psf_stamp(
    sigma_major_px: float,
    sigma_minor_px: float,
    theta_rad: float,
    size: int,
) -> np.ndarray:
    """Return a unit-sum, centred, elliptical Gaussian PSF on a square odd grid.

    Args:
        sigma_major_px: Sigma along the major axis (pixels).
        sigma_minor_px: Sigma along the minor axis (pixels).
        theta_rad: Major-axis position angle, radians CCW from +x.
        size: Stamp side length; forced odd.

    Returns:
        ``(size, size)`` float64 array summing to 1.0.
    """
    size = int(size) | 1
    half = size // 2
    y, x = np.mgrid[-half : half + 1, -half : half + 1]
    ct, st = np.cos(float(theta_rad)), np.sin(float(theta_rad))
    xr = x * ct + y * st
    yr = -x * st + y * ct
    psf = np.exp(
        -0.5 * ((xr / float(sigma_major_px)) ** 2 + (yr / float(sigma_minor_px)) ** 2)
    )
    return (psf / psf.sum()).astype(np.float64)


def _kernel_basis(
    basis_sigmas_px: tuple[float, ...],
    degrees: tuple[int, ...],
    kernel_size: int,
) -> tuple[list[np.ndarray], list[tuple[float, int, int]]]:
    """Build the (Gaussian x monomial) kernel basis images.

    Returns:
        (basis_images, meta) where meta[i] = (sigma_px, p, q) for basis_images[i].
    """
    if len(basis_sigmas_px) != len(degrees):
        raise ValueError("basis_sigmas_px and degrees must have equal length")
    kernel_size = int(kernel_size) | 1
    half = kernel_size // 2
    y, x = np.mgrid[-half : half + 1, -half : half + 1]
    x = x.astype(np.float64)
    y = y.astype(np.float64)

    basis: list[np.ndarray] = []
    meta: list[tuple[float, int, int]] = []
    for sigma, degree in zip(basis_sigmas_px, degrees):
        gauss = np.exp(-(x**2 + y**2) / (2.0 * float(sigma) ** 2))
        for p in range(int(degree) + 1):
            for q in range(int(degree) + 1 - p):
                basis.append((x**p) * (y**q) * gauss)
                meta.append((float(sigma), p, q))
    return basis, meta


def solve_matching_kernel(
    source_psf: np.ndarray,
    target_psf: np.ndarray,
    basis_sigmas_px: tuple[float, ...] = DEFAULT_BASIS_SIGMAS_PX,
    degrees: tuple[int, ...] = DEFAULT_DEGREES,
    kernel_size: int | None = None,
) -> tuple[np.ndarray, np.ndarray, list[tuple[float, int, int]]]:
    """Least-squares fit of the A&L kernel that takes source_psf to target_psf.

    Args:
        source_psf: Better-seeing (sharper) model PSF, square 2-D, any norm.
        target_psf: Desired output PSF, same shape as ``source_psf``.
        basis_sigmas_px: Gaussian basis widths in pixels.
        degrees: Per-Gaussian polynomial degree (p + q <= degree).
        kernel_size: Kernel side length (odd). Defaults to ~8*max(sigma)+1,
            clipped to the PSF stamp size.

    Returns:
        (kernel, coeffs, meta): the fitted ``(kernel_size, kernel_size)`` kernel,
        the basis coefficients, and per-basis ``(sigma_px, p, q)`` metadata.
    """
    source_psf = np.asarray(source_psf, dtype=np.float64)
    target_psf = np.asarray(target_psf, dtype=np.float64)
    if source_psf.shape != target_psf.shape:
        raise ValueError("source_psf and target_psf must have the same shape")
    if source_psf.ndim != 2 or source_psf.shape[0] != source_psf.shape[1]:
        raise ValueError("PSF stamps must be square 2-D arrays")

    n = source_psf.shape[0]
    if kernel_size is None:
        kernel_size = 2 * int(round(4.0 * max(basis_sigmas_px))) + 1
    kernel_size = int(kernel_size) | 1
    if kernel_size > n:
        kernel_size = n if n % 2 == 1 else n - 1

    from scipy.signal import fftconvolve

    basis, meta = _kernel_basis(basis_sigmas_px, degrees, kernel_size)
    design = np.column_stack(
        [fftconvolve(source_psf, b, mode="same").ravel() for b in basis]
    )
    coeffs, _res, _rank, _sv = np.linalg.lstsq(design, target_psf.ravel(), rcond=None)

    kernel = np.zeros((kernel_size, kernel_size), dtype=np.float64)
    for c, b in zip(coeffs, basis):
        kernel += c * b
    return kernel, coeffs, meta


def match_psf_al(
    data: np.ndarray,
    source_psf: np.ndarray,
    target_psf: np.ndarray,
    *,
    kernel_size: int | None = None,
    basis_sigmas_px: tuple[float, ...] = DEFAULT_BASIS_SIGMAS_PX,
    degrees: tuple[int, ...] = DEFAULT_DEGREES,
) -> tuple[np.ndarray, dict]:
    """Convolve ``data`` by the A&L kernel that matches source_psf to target_psf.

    Native pixel space only — call before reprojection, exactly like the
    ``gaussian-fft`` path.

    Args:
        data: 2-D image to PSF-match.
        source_psf: This frame's model PSF (square 2-D).
        target_psf: Epoch target PSF (same shape as ``source_psf``).
        kernel_size: Optional odd kernel side length.
        basis_sigmas_px: Gaussian basis widths in pixels.
        degrees: Per-Gaussian polynomial degrees.

    Returns:
        (matched_data float32, info) where ``info`` records the method, kernel
        size, basis widths, kernel sum, negative-lobe fraction, and the
        white-noise variance-inflation factor ``sum(K^2) / sum(K)^2``.
    """
    from astropy.convolution import convolve_fft

    kernel, _coeffs, meta = solve_matching_kernel(
        source_psf, target_psf, basis_sigmas_px, degrees, kernel_size
    )

    matched = convolve_fft(
        np.asarray(data, dtype=np.float64),
        kernel,
        allow_huge=True,
        normalize_kernel=False,
    )

    ksum = float(kernel.sum())
    abs_total = float(np.abs(kernel).sum())
    info = {
        "method": "alard-lupton",
        "kernel_size": int(kernel.shape[0]),
        "basis_sigmas_px": tuple(float(s) for s in basis_sigmas_px),
        "degrees": tuple(int(d) for d in degrees),
        "n_terms": len(meta),
        "kernel_sum": ksum,
        "kernel_neg_fraction": (
            float(np.abs(kernel[kernel < 0]).sum() / abs_total) if abs_total else 0.0
        ),
        "variance_inflation": (
            float(np.sum(kernel**2) / ksum**2) if ksum != 0.0 else float("nan")
        ),
    }
    return matched.astype(np.float32), info
