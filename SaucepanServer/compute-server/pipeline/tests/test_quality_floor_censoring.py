"""Regression test for #464: background.py's floor clip must not collapse
quality.py's whole-image noise/SNR estimate to zero.

subtract_background() clips negative background-subtracted pixels to
exactly 0 (a legitimate physical floor - counts can't be negative). For any
background-noise-dominated image this ties close to half of all pixels to
that identical value, which is routine, not a pathological edge case. Before
the #464 fix, assess_quality()'s whole-image median()/MAD() collapsed to 0
whenever that tied population crossed ~50%, which cascaded into
misclassifying every remaining background pixel as a "star" and reporting
SNR as exactly zero - and silently disabled combine.py's inverse-variance
stack weighting for any affected frame (base_weight falls back to 1.0 when
noise_adu is 0).
"""

from __future__ import annotations

import numpy as np
from saucepan_pipeline.background import subtract_background
from saucepan_pipeline.quality import assess_quality


def _sparse_field(rng: np.random.Generator, h: int = 200, w: int = 200) -> np.ndarray:
    """Background-dominated field: one compact source, mostly sky noise -
    deliberately sparse, since a sparse (not crowded) field is exactly the
    realistic case that triggers #464, not an artificial extreme."""
    sky_rate = 240.0
    data = rng.poisson(sky_rate, (h, w)).astype(np.float32)
    data += rng.normal(0.0, 1.2, size=(h, w)).astype(np.float32)

    yy, xx = np.mgrid[0:h, 0:w]
    sigma_px = 2.0 / 2.3548
    cx, cy = w / 2.0, h / 2.0
    star = 6000.0 * np.exp(-(((xx - cx) ** 2 + (yy - cy) ** 2) / (2 * sigma_px**2)))
    data += star.astype(np.float32)
    return data.astype(np.float32)


def test_background_subtraction_ties_close_to_half_the_image():
    """Confirms the precondition this test exists to guard against: without
    the #464 fix, this is exactly the scenario that breaks quality
    assessment - not a contrived extreme."""
    rng = np.random.default_rng(7)
    data = _sparse_field(rng)
    bg_sub, _bg_map = subtract_background(data, box_size=50)
    frac_at_floor = float((bg_sub == 0).mean())
    assert frac_at_floor > 0.3, (
        "test fixture no longer reproduces a large tied-at-floor population - "
        "update the fixture, don't just drop the assertion"
    )


def test_quality_assessment_survives_floor_censoring():
    """The actual regression: even with a large tied-at-floor population,
    assess_quality() must return a real, non-degenerate noise/SNR reading,
    not the exactly-zero collapse described in #464."""
    rng = np.random.default_rng(7)
    data = _sparse_field(rng)
    bg_sub, _bg_map = subtract_background(data, box_size=50)

    result = assess_quality(bg_sub, pixel_scale_arcsec=1.2)

    assert result["noise_adu"] > 0, (
        "noise_adu collapsed to zero - the #464 floor-censoring bug has regressed"
    )
    assert result["snr"] > 0
    # Before the fix this was exactly half the image (every unclipped
    # background pixel misclassified as a star); a real compact source
    # should flag a small fraction of pixels, not ~50%.
    assert result["star_pixels"] < data.size * 0.1


def test_quality_assessment_matches_uncensored_reference_within_tolerance():
    """The fixed estimate should be statistically close to what the same
    algorithm reports on equivalent data that was never floor-clipped -
    proving this is a real fix, not just "no longer exactly zero"."""
    rng = np.random.default_rng(7)
    data = _sparse_field(rng)

    bg_sub, bg_map = subtract_background(data, box_size=50)
    censored = assess_quality(bg_sub, pixel_scale_arcsec=1.2)

    uncensored_equivalent = (data - bg_map).astype(np.float32)  # no clip
    uncensored = assess_quality(uncensored_equivalent, pixel_scale_arcsec=1.2)

    assert uncensored["noise_adu"] > 0
    ratio = censored["noise_adu"] / uncensored["noise_adu"]
    assert 0.5 < ratio < 2.0, (
        f"censored noise_adu={censored['noise_adu']} vs uncensored "
        f"{uncensored['noise_adu']} diverge too much to be the same signal"
    )
