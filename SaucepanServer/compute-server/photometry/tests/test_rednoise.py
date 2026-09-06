"""Pont 2006 time-binned red-noise estimate (#206)."""

from __future__ import annotations

import math
import random

import pytest
from photometry import rednoise

_SPAN_MIN = 240.0
_BIN = 10.0


def _times(n: int) -> list[float]:
    return [i * (_SPAN_MIN / n) for i in range(n)]


def test_white_noise_is_white_limited():
    rng = random.Random(206)
    n = 240
    t = _times(n)
    resid = [rng.gauss(0.0, 0.01) for _ in range(n)]
    out = rednoise.pont_red_noise(t, resid, bin_width=_BIN, sigma_white=0.01)
    assert out is not None
    assert out["beta"] == pytest.approx(1.0, abs=0.6)
    assert out["sigma_red"] < 0.006
    assert out["red_noise_detected"] is False


def test_correlated_drift_shows_red_noise():
    rng = random.Random(206)
    n = 240
    t = _times(n)
    # slow sinusoidal systematic + small white noise
    resid = [
        0.03 * math.sin(2.0 * math.pi * ti / _SPAN_MIN) + rng.gauss(0.0, 0.005)
        for ti in t
    ]
    out = rednoise.pont_red_noise(t, resid, bin_width=_BIN, sigma_white=0.005)
    assert out["beta"] > 3.0
    assert out["sigma_red"] > 0.01
    assert out["red_noise_detected"] is True


def test_binned_rms_drops_underfilled_bins():
    t = [0.0, 1.0, 2.0, 100.0]  # last bin has a single point
    b = rednoise.binned_rms(t, [0.1, -0.1, 0.05, 5.0], _BIN, min_per_bin=2)
    assert b["n_bins"] == 1
    assert b["rms"] is None  # < 2 usable bins


def test_fails_closed_on_short_series():
    assert rednoise.pont_red_noise([0.0], [0.1], bin_width=_BIN) is None


def test_sigma_white_defaults_to_unbinned_stdev():
    rng = random.Random(7)
    t = _times(120)
    resid = [rng.gauss(0.0, 0.02) for _ in t]
    out = rednoise.pont_red_noise(t, resid, bin_width=_BIN)
    assert out["sigma_white"] == pytest.approx(0.02, abs=0.006)
