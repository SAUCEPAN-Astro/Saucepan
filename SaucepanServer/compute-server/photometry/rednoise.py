"""Time-correlated ("red") noise estimate, Pont, Zucker & Queloz (2006) style.

Photon/sky/read noise average down as ``1/sqrt(n)`` when a light curve is
binned; correlated systematics (transparency, tracking, flat-field residuals)
do not. Pont 2006 measures this by comparing the RMS of the binned residuals
to what pure white noise predicts:

    sigma_red^2(t_bin) = max(0, RMS(binned residuals)^2 - sigma_white^2 / n_bin)
    beta(t_bin)        = RMS(binned residuals) / (sigma_white / sqrt(n_bin))

``beta ~ 1`` means white-noise-limited; ``beta > 1`` flags red noise, and
``sigma_red`` is the extra term to add to the error budget (#206) at that
binning timescale.

If the researcher supplies no residual series, the ``red`` budget term is
``null`` and the product metadata must say so — this module does not invent a
value from nothing.
"""

from __future__ import annotations

import math
import statistics
import typing


def _bin_indices(times: typing.Sequence[float], bin_width: float) -> dict[int, list[int]]:
    t0 = min(times)
    groups: dict[int, list[int]] = {}
    for i, t in enumerate(times):
        groups.setdefault(int(math.floor((t - t0) / bin_width)), []).append(i)
    return groups


def binned_rms(
    times: typing.Sequence[float],
    residuals: typing.Sequence[float],
    bin_width: float,
    *,
    min_per_bin: int = 2,
) -> dict[str, typing.Any]:
    """RMS of bin-averaged residuals at a single binning timescale.

    ``times`` and ``residuals`` share units of the caller's choosing (minutes /
    mag is typical). Bins with fewer than ``min_per_bin`` points are dropped.
    Returns ``{"bin_width", "n_bins", "mean_n_per_bin", "rms", "bin_means"}``;
    ``rms`` is ``None`` when fewer than two usable bins remain.
    """
    if len(times) != len(residuals):
        raise ValueError("times and residuals must be the same length")
    if bin_width <= 0.0:
        raise ValueError("bin_width must be > 0")

    groups = _bin_indices(times, bin_width)
    bin_means = [
        statistics.fmean(residuals[i] for i in idx)
        for idx in groups.values()
        if len(idx) >= min_per_bin
    ]
    counts = [len(idx) for idx in groups.values() if len(idx) >= min_per_bin]
    rms = (
        math.sqrt(statistics.fmean(v * v for v in bin_means))
        if len(bin_means) >= 2
        else None
    )
    return {
        "bin_width": bin_width,
        "n_bins": len(bin_means),
        "mean_n_per_bin": statistics.fmean(counts) if counts else 0.0,
        "rms": rms,
        "bin_means": bin_means,
    }


def pont_red_noise(
    times: typing.Sequence[float],
    residuals: typing.Sequence[float],
    *,
    bin_width: float,
    sigma_white: float | None = None,
    min_per_bin: int = 2,
) -> dict[str, typing.Any] | None:
    """Pont 2006 red-noise term at one binning timescale.

    ``sigma_white`` defaults to the unbinned residual standard deviation. The
    result's ``sigma_red`` is the term to add in quadrature to the #206 budget
    for measurements averaged over ``bin_width``.

    Returns ``None`` (fail closed) when there is not enough data — fewer than
    two usable bins, or a non-finite ``sigma_white``.
    """
    if len(residuals) < 2:
        return None
    sw = float(sigma_white) if sigma_white is not None else statistics.pstdev(residuals)
    if not math.isfinite(sw) or sw < 0.0:
        return None

    b = binned_rms(times, residuals, bin_width, min_per_bin=min_per_bin)
    if b["rms"] is None:
        return None

    n = b["mean_n_per_bin"]
    expected_white = sw / math.sqrt(n) if n > 0 else float("inf")
    sigma_red = math.sqrt(max(0.0, b["rms"] ** 2 - expected_white**2))
    beta = b["rms"] / expected_white if expected_white > 0.0 else None
    return {
        "bin_width": bin_width,
        "n_bins": b["n_bins"],
        "mean_n_per_bin": n,
        "sigma_white": sw,
        "expected_white_binned": expected_white,
        "rms_binned": b["rms"],
        "sigma_red": sigma_red,
        "beta": beta,
        "red_noise_detected": bool(beta is not None and beta > 1.0 and sigma_red > 0.0),
    }
