"""psf_match_al.py — Alard & Lupton (1998) kernel-matching backend (#309).

Native pixel space, before reprojection. Covers:
  * elongated PSF pair -> A&L cross-elongation residual beats the isotropic
    gaussian-fft kernel;
  * round PSF pair -> A&L and gaussian-fft agree within tolerance;
  * a default match_psf() call (no method=) is byte-identical to the frozen
    gaussian-fft computation;
  * write_psf_headers() / handler() record the method actually used + kernel
    params.
"""

from __future__ import annotations

import base64

import numpy as np
import pytest
from astropy.convolution import Gaussian2DKernel, convolve_fft
from astropy.io import fits
from scipy.signal import fftconvolve
from saucepan_pipeline.psf_match import (
    handler,
    match_psf,
    write_psf_headers,
)
from saucepan_pipeline.psf_match_al import (
    elliptical_gaussian_psf_stamp,
    gaussian_psf_stamp,
    match_psf_al,
    solve_matching_kernel,
)

STAMP = 41


def _star_image(size=96, fwhm_px=3.0, seed=0) -> np.ndarray:
    rng = np.random.default_rng(seed)
    data = np.full((size, size), 100.0, dtype=np.float32)
    data += rng.normal(0, 1.0, size=(size, size)).astype(np.float32)
    sigma = fwhm_px / 2.3548
    yy, xx = np.mgrid[:size, :size]
    data += (
        500.0 * np.exp(-(((xx - size / 2) ** 2 + (yy - size / 2) ** 2) / (2 * sigma**2)))
    ).astype(np.float32)
    return data


def _isotropic_match(source_psf: np.ndarray, sigma_kernel_px: float) -> np.ndarray:
    """Reproduce the gaussian-fft path on a PSF stamp: one isotropic kernel."""
    kernel = Gaussian2DKernel(x_stddev=sigma_kernel_px)
    return convolve_fft(source_psf, kernel, allow_huge=True)


def _profile_resid(model: np.ndarray, target: np.ndarray, axis: int) -> float:
    """Normalised RMS residual along a central 1-D cut (axis=1 -> x profile)."""
    c = target.shape[0] // 2
    mp = model[c, :] if axis == 1 else model[:, c]
    tp = target[c, :] if axis == 1 else target[:, c]
    return float(np.sqrt(np.mean((mp - tp) ** 2)) / tp.max())


# --- elongated pair: A&L beats the isotropic kernel on the cross axis --------


def test_alard_lupton_beats_gaussian_fft_on_elongated_pair() -> None:
    """Source elongated along x; target rounder. The isotropic
    sqrt(sigma_T^2 - sigma_S^2) kernel cannot un-elongate the source, so it
    leaves a large residual on the major (x) axis. The A&L kernel is fitted and
    can be anisotropic, so its cross-elongation residual is far smaller."""
    source_psf = elliptical_gaussian_psf_stamp(2.6, 1.4, 0.0, STAMP)
    target_psf = gaussian_psf_stamp(2.8, STAMP)

    # gaussian-fft equivalent: isotropic kernel from a scalar sigma pair, using
    # the geometric-mean source sigma as the caller's single "source FWHM".
    src_sigma_eff = np.sqrt(2.6 * 1.4)
    kernel_sigma = float(np.sqrt(2.8**2 - src_sigma_eff**2))
    gfft_model = _isotropic_match(source_psf, kernel_sigma)
    gfft_x = _profile_resid(gfft_model, target_psf, axis=1)

    kernel, _coeffs, _meta = solve_matching_kernel(source_psf, target_psf)
    al_model = fftconvolve(source_psf, kernel, mode="same")
    al_x = _profile_resid(al_model, target_psf, axis=1)

    assert al_x < gfft_x
    assert al_x < 0.05  # A&L actually matches the target PSF
    # overall 2-D residual also improves
    al_rms = float(np.sqrt(np.mean((al_model - target_psf) ** 2)) / target_psf.max())
    gfft_rms = float(np.sqrt(np.mean((gfft_model - target_psf) ** 2)) / target_psf.max())
    assert al_rms < gfft_rms


# --- round pair: the two methods agree -------------------------------------


def test_alard_lupton_agrees_with_gaussian_fft_on_round_pair() -> None:
    """For circular PSFs the fitted A&L kernel should reproduce the isotropic
    gaussian-fft broadening: matched images agree to a fraction of a percent."""
    data = _star_image(fwhm_px=2.0, seed=3) - 100.0  # background-subtracted

    gfft = match_psf(data, 2.0, 5.0, 1.0, method="gaussian-fft")
    al = match_psf(data, 2.0, 5.0, 1.0, method="alard-lupton")

    assert al.shape == gfft.shape
    assert al.dtype == np.float32
    peak = float(np.abs(gfft).max())
    max_abs_diff = float(np.abs(al - gfft).max())
    assert max_abs_diff / peak < 0.02
    # flux conserved to the same tolerance as the gaussian-fft path
    assert al.sum() == pytest.approx(data.sum(), rel=0.05)


# --- default call is byte-identical to the frozen gaussian-fft computation --


def test_default_call_is_byte_identical_to_frozen_gaussian_fft() -> None:
    data = _star_image(fwhm_px=2.0, seed=5)

    # Frozen reproduction of today's match_psf internals.
    source_fwhm_px = 2.0 / 1.0
    target_fwhm_px = 5.0 / 1.0
    fwhm_to_sigma = 1.0 / 2.3548
    kernel_sigma = np.sqrt(
        (target_fwhm_px * fwhm_to_sigma) ** 2 - (source_fwhm_px * fwhm_to_sigma) ** 2
    )
    frozen = convolve_fft(
        data, Gaussian2DKernel(x_stddev=kernel_sigma), allow_huge=True
    ).astype(np.float32)

    default = match_psf(data, 2.0, 5.0, 1.0)
    explicit = match_psf(data, 2.0, 5.0, 1.0, method="gaussian-fft")

    assert np.array_equal(default, frozen)
    assert np.array_equal(default, explicit)


def test_match_psf_rejects_unknown_method() -> None:
    data = _star_image()
    with pytest.raises(ValueError, match="Unknown PSF match method"):
        match_psf(data, 2.0, 5.0, 1.0, method="wiener")


# --- headers / handler record the method + kernel params -------------------


def test_write_psf_headers_records_alard_lupton_method_and_params() -> None:
    header = fits.Header()
    write_psf_headers(
        header,
        source_fwhm=2.0,
        target_fwhm=4.0,
        kernel_sigma_px=1.5,
        method="alard-lupton",
        kernel_size=25,
        basis_sigmas_px=(1.0, 2.0, 3.0),
    )
    assert header["SP_PSF_MATCH"] is True
    assert header["SP_PSF_METH"] == "alard-lupton"
    assert header["SP_PSF_KSZ"] == 25
    assert header["SP_PSF_BW"] == "1,2,3"


def test_write_psf_headers_default_still_gaussian_fft_without_al_keys() -> None:
    header = fits.Header()
    write_psf_headers(header, source_fwhm=2.0, target_fwhm=4.0, kernel_sigma_px=1.5)
    assert header["SP_PSF_METH"] == "gaussian-fft"
    assert "SP_PSF_KSZ" not in header
    assert "SP_PSF_BW" not in header


def test_handler_alard_lupton_records_method_and_kernel_metadata() -> None:
    data = _star_image(fwhm_px=2.0, seed=7)
    payload = {
        "action": "match_psf",
        "data_base64": base64.b64encode(data.tobytes()).decode(),
        "shape": list(data.shape),
        "source_fwhm": 2.0,
        "target_fwhm": 5.0,
        "pixel_scale": 1.0,
        "method": "alard-lupton",
    }
    result = handler(payload)
    assert result["statusCode"] == 200
    meta = result["body"]["metadata"]
    assert meta["method"] == "alard-lupton"
    assert meta["kernel_size"] % 2 == 1
    assert meta["basis_sigmas_px"] == [1.0, 2.0, 3.0]
    assert "variance_inflation" in meta
    out = np.frombuffer(
        base64.b64decode(result["body"]["data_base64"]), dtype=np.float32
    ).reshape(data.shape)
    assert out.shape == data.shape


def test_handler_default_method_is_gaussian_fft() -> None:
    data = _star_image(fwhm_px=2.0, seed=8)
    payload = {
        "action": "match_psf",
        "data_base64": base64.b64encode(data.tobytes()).decode(),
        "shape": list(data.shape),
        "source_fwhm": 2.0,
        "target_fwhm": 5.0,
        "pixel_scale": 1.0,
    }
    result = handler(payload)
    assert result["statusCode"] == 200
    assert result["body"]["metadata"]["method"] == "gaussian-fft"


# --- noise caveat is quantified in the returned info ----------------------


def test_match_psf_al_reports_variance_inflation() -> None:
    """The A&L info dict must expose the white-noise variance-inflation factor
    sum(K^2)/sum(K)^2 and the negative-lobe fraction, so #298's
    inverse-variance weights can use the post-match noise."""
    data = _star_image(fwhm_px=2.0, seed=9) - 100.0
    src = gaussian_psf_stamp(2.0 / 2.3548, STAMP)
    tgt = gaussian_psf_stamp(5.0 / 2.3548, STAMP)
    _matched, info = match_psf_al(data, src, tgt)
    assert info["method"] == "alard-lupton"
    assert info["variance_inflation"] > 0.0
    assert 0.0 <= info["kernel_neg_fraction"] < 1.0
    assert info["kernel_sum"] == pytest.approx(1.0, abs=0.05)
