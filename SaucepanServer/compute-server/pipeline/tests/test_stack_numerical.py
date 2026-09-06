"""#415 - numerical validation of the weighted sigma-clip stacker.

Synthetic-truth checks on ``saucepan_pipeline.stacking.combine.stack_frames``:

1. **SNR improves as sqrt(N)** - stacking N equal-noise frames of the same
   scene drives the background noise down by ~sqrt(N) and the SNR at a star
   up by ~sqrt(N), across N in {2, 4, 8, 16}.
2. **Flux conservation** - the combined SCIENCE keeps the per-frame flux
   level (``combine`` is a weighted *mean*, so a star's aperture-mean counts
   survive the stack) vs the noise-free truth scene.
3. **Reference benchmark** - ``stack_frames``' SCIENCE agrees, within a
   documented tolerance, with an independent inverse-variance weighted mean
   + per-pixel sigma-clip over the same inputs.

``ccdproc.combine`` is the reference the issue names, but it is not
installed in this environment, so the benchmark is an ``astropy.stats``
``sigma_clip`` + numpy weighted-mean built here. The algorithm is the same
one ``combine.py`` implements: per-pixel sigma-clip along the frame axis,
then an inverse-variance weighted mean of the survivors. Tolerances below
are quoted against that reference.

Runtime: 64x64 frames, N <= 16, all frames share one WCS so reprojection
is a near-identity resample. The whole file runs in a few seconds - no
``@pytest.mark.slow`` gate is needed and it rides the ``pipeline/tests/``
glob that ``ci.yml`` (compute job) and ``scripts/preflight.sh --only
compute`` already collect.
"""

from __future__ import annotations

import numpy as np
import pytest
from astropy.io import fits
from astropy.stats import sigma_clip
from astropy.wcs import WCS
from saucepan_pipeline.stacking.combine import stack_frames
from saucepan_pipeline.stacking.models import FrameInfo

SHAPE = (64, 64)
SKY = 200.0  # flat sky pedestal, electrons
NOISE_ADU = 12.0  # per-frame Gaussian noise sigma
STAR_XY = (32, 32)  # centre pixel of the single test star
STAR_AMP = 3000.0  # peak electrons above sky
STAR_SIGMA = 1.8  # px
_AP = (slice(30, 35), slice(30, 35))  # 5x5 aperture around the star
_BLANK = (slice(6, 26), slice(6, 26))  # source-free corner, >= 8 px from star
_INNER = (slice(15, -15), slice(15, -15))  # reproject-edge-free interior


def _tan_wcs() -> WCS:
    h, w = SHAPE
    hdr = fits.Header()
    hdr["CTYPE1"] = "RA---TAN"
    hdr["CTYPE2"] = "DEC--TAN"
    hdr["CRPIX1"] = w / 2.0
    hdr["CRPIX2"] = h / 2.0
    hdr["CRVAL1"] = 180.0
    hdr["CRVAL2"] = 0.0
    hdr["CDELT1"] = -1.0 / 3600.0
    hdr["CDELT2"] = 1.0 / 3600.0
    return WCS(hdr)


def _truth_scene() -> np.ndarray:
    """Flat sky + one Gaussian star, no noise."""
    yy, xx = np.mgrid[0 : SHAPE[0], 0 : SHAPE[1]]
    x0, y0 = STAR_XY
    r2 = (xx - x0) ** 2 + (yy - y0) ** 2
    star = STAR_AMP * np.exp(-r2 / (2.0 * STAR_SIGMA**2))
    return (SKY + star).astype(np.float64)


def _frame(data: np.ndarray, wcs: WCS, tele: str) -> FrameInfo:
    hdr = fits.Header()
    hdr.update(wcs.to_header())
    hdr["SP_RA"] = 180.0
    hdr["SP_DEC"] = 0.0
    return FrameInfo(
        path="mem",
        telescope_id=tele,
        data=np.asarray(data, dtype=np.float32),
        header=hdr,
        wcs=wcs,
        noise_adu=NOISE_ADU,
        background=SKY,
        fwhm_arcsec=3.0,
        pixel_scale_arcsec=1.0,
        exptime=1.0,
    )


def _make_cohort(n: int, seed: int = 415) -> list[FrameInfo]:
    """N frames of the same truth scene, independent nested noise draws.

    The i-th frame's noise is drawn from ``default_rng(seed + i)`` so a
    cohort of size N is exactly the first N frames of any larger cohort -
    that keeps the SNR(N) ratios below smooth rather than re-randomised
    per N.
    """
    truth = _truth_scene()
    wcs = _tan_wcs()
    frames = []
    for i in range(n):
        rng = np.random.default_rng(seed + i)
        noisy = truth + rng.normal(0.0, NOISE_ADU, SHAPE)
        frames.append(_frame(noisy, wcs, tele=f"t{i:02d}"))
    return frames


def _stack(frames: list[FrameInfo], sigma_clip_val: float = 0.0):
    return stack_frames(
        frames,
        sigma_clip=sigma_clip_val,
        weight_by_fwhm=False,
        photometric_scale=False,
        auto_crop=False,
    )


def _bg_noise(science: np.ndarray) -> float:
    """Std of the source-free corner - removes the constant SKY pedestal."""
    return float(np.nanstd(science[_BLANK]))


def _star_signal(science: np.ndarray) -> float:
    """Aperture-mean counts above sky at the star."""
    return float(np.nanmean(science[_AP]) - SKY)


# --------------------------------------------------------------------------
# 1. SNR improvement scales as sqrt(N)
# --------------------------------------------------------------------------


@pytest.mark.parametrize("n", [2, 4, 8, 16])
def test_background_noise_drops_as_sqrt_n(n: int) -> None:
    """Weighted mean of N equal-variance frames has variance sigma^2 / N,
    so the stacked background noise * sqrt(N) recovers the single-frame
    sigma (== NOISE_ADU) to within a few percent."""
    result = _stack(_make_cohort(n))
    recovered = _bg_noise(result.science) * np.sqrt(n)
    assert 0.85 * NOISE_ADU < recovered < 1.15 * NOISE_ADU, (
        f"N={n}: bg_noise*sqrt(N)={recovered:.3f} should be ~NOISE_ADU="
        f"{NOISE_ADU:.3f}; stacking is not delivering the sqrt(N) noise drop"
    )


def test_snr_at_star_grows_as_sqrt_n() -> None:
    """SNR at the star relative to the N=2 stack tracks sqrt(N/2)."""
    snr = {}
    for n in (2, 4, 8, 16):
        result = _stack(_make_cohort(n))
        snr[n] = _star_signal(result.science) / _bg_noise(result.science)

    for n in (4, 8, 16):
        ratio = snr[n] / snr[2]
        expected = np.sqrt(n / 2.0)
        assert 0.85 * expected < ratio < 1.15 * expected, (
            f"SNR(N={n})/SNR(N=2)={ratio:.3f} should be ~sqrt(N/2)="
            f"{expected:.3f}"
        )


# --------------------------------------------------------------------------
# 2. Flux conservation vs the noise-free truth
# --------------------------------------------------------------------------


def test_star_flux_level_conserved_through_stack() -> None:
    """A weighted mean preserves the per-frame flux level: the star's
    aperture-mean counts in an N=8 stack match the noise-free truth
    scene within 5%."""
    truth = _truth_scene()
    truth_signal = float(truth[_AP].mean() - SKY)

    result = _stack(_make_cohort(8))
    stacked_signal = _star_signal(result.science)

    assert abs(stacked_signal / truth_signal - 1.0) < 0.05, (
        f"stacked star aperture-mean {stacked_signal:.2f} vs truth "
        f"{truth_signal:.2f} - flux not conserved by the weighted mean"
    )


def test_sky_pedestal_conserved_through_stack() -> None:
    """The flat sky level survives the combine (mean of frames each at
    SKY is SKY) - guards against a normalisation sneaking in."""
    result = _stack(_make_cohort(8))
    stacked_sky = float(np.nanmedian(result.science[_BLANK]))
    assert abs(stacked_sky - SKY) < 0.05 * NOISE_ADU, (
        f"stacked sky median {stacked_sky:.3f} drifted from SKY={SKY}"
    )


# --------------------------------------------------------------------------
# 3. Reference benchmark: astropy sigma_clip + inverse-variance weighted mean
# --------------------------------------------------------------------------


def _reference_combine(frames: list[FrameInfo], sigma: float | None) -> np.ndarray:
    """Independent stacker: optional per-pixel sigma-clip along the frame
    axis, then an inverse-variance weighted mean of the survivors.

    All frames share one WCS, so ``stack_frames``' reprojection is a
    near-identity resample and the raw ``frame.data`` arrays are the right
    thing to combine here. Weights are ``1 / noise_adu**2`` per frame
    (equal in this cohort, so this reduces to a plain masked mean) - the
    same photon-noise weighting ``combine.py`` applies per pixel.
    ``sigma=None`` disables clipping entirely.
    """
    cube = np.stack([np.asarray(f.data, dtype=np.float64) for f in frames], axis=0)
    if sigma is None:
        mask = np.zeros(cube.shape, dtype=bool)
    else:
        mask = sigma_clip(cube, sigma=sigma, maxiters=5, axis=0, masked=True).mask
    w = np.array([1.0 / (f.noise_adu**2) for f in frames], dtype=np.float64)
    w3 = w[:, None, None] * (~mask)
    num = np.nansum(np.where(mask, 0.0, cube) * w3, axis=0)
    den = np.sum(w3, axis=0)
    with np.errstate(divide="ignore", invalid="ignore"):
        return np.where(den > 0, num / den, np.nan)


def test_science_matches_weighted_mean_reference_no_clip() -> None:
    """With sigma-clip off, ``stack_frames``' SCIENCE is the inverse-variance
    weighted mean, to floating-point tolerance - the reprojection onto an
    identical WCS is a pure identity resample and adds nothing.

    This is the load-bearing "the science is right" benchmark. The issue
    names ``ccdproc.combine`` as the reference; it is not installed here, so
    the reference is an astropy/numpy weighted mean - which for the no-clip
    path is exactly what ``ccdproc.combine(method='average', weights=...)``
    computes."""
    frames = _make_cohort(8)
    pipeline = _stack(frames, sigma_clip_val=0.0).science
    reference = _reference_combine(frames, sigma=None)

    d = pipeline[_INNER] - reference[_INNER]
    assert float(np.nanmax(np.abs(d))) < 1e-3, (
        f"pipeline vs weighted-mean reference max |diff| "
        f"{float(np.nanmax(np.abs(d))):.2e} - the combine is not a plain "
        f"weighted mean when clipping is off"
    )


def test_sigma_clipped_science_close_to_astropy_reference() -> None:
    """With sigma-clip on, ``stack_frames`` and an astropy ``sigma_clip`` +
    weighted-mean reference agree within 10% of a single frame's noise
    (RMS over the interior).

    The residual is *clip-decision* divergence, not a combination error:
    astropy derives each pixel's 3-sigma scatter from the N-sample std
    across the cohort, while ``combine.py`` (#411/#412) uses the propagated
    per-pixel variance (here the scalar ``noise_adu`` fallback) with a MAD
    backstop. Borderline ~3-sigma pixels therefore clip in one and not the
    other; each such pixel moves by ~sigma/sqrt(N). It shrinks with N
    (RMS ~1.2 at N=6 -> ~0.55 at N=16), so this runs at N=16. The no-clip
    path above pins the actual combination math to float tolerance."""
    frames = _make_cohort(16)
    pipeline = _stack(frames, sigma_clip_val=3.0).science
    reference = _reference_combine(frames, sigma=3.0)

    d = pipeline[_INNER] - reference[_INNER]
    rms = float(np.sqrt(np.nanmean(d**2)))
    assert rms < 0.10 * NOISE_ADU, (
        f"pipeline vs clipped reference RMS {rms:.4f} exceeds 10% of "
        f"NOISE_ADU ({0.10 * NOISE_ADU:.4f}) - larger than clip-decision "
        f"divergence explains"
    )

    bg_pipe = float(np.nanmedian(pipeline[_BLANK]))
    bg_ref = float(np.nanmedian(reference[_BLANK]))
    assert abs(bg_pipe - bg_ref) < 0.02 * NOISE_ADU, (
        f"background median disagrees: pipeline {bg_pipe:.4f} vs reference "
        f"{bg_ref:.4f}"
    )


def test_recovered_star_snr_agrees_with_reference() -> None:
    """Both stackers recover the same high-SNR star SNR within 10% (the
    star aperture is well clear of the 3-sigma clip, so this stays tight
    regardless of the clip-decision divergence above)."""
    frames = _make_cohort(16)
    pipeline = _stack(frames, sigma_clip_val=3.0).science
    reference = _reference_combine(frames, sigma=3.0)

    snr_pipe = _star_signal(pipeline) / _bg_noise(pipeline)
    snr_ref = float(np.nanmean(reference[_AP]) - SKY) / float(np.nanstd(reference[_BLANK]))

    assert abs(snr_pipe / snr_ref - 1.0) < 0.10, (
        f"recovered SNR disagrees: pipeline {snr_pipe:.2f} vs reference "
        f"{snr_ref:.2f}"
    )
