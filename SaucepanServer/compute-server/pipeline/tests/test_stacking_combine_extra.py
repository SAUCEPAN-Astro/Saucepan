"""stacking/combine.py — additional edge cases for
estimate_photometric_scales() beyond test_pipeline_driver.py's happy-path
coverage: too-few-positive-pixel frames and all-non-positive-flux frames.
"""

from __future__ import annotations

import numpy as np
from saucepan_pipeline.stacking.combine import estimate_photometric_scales


def test_estimate_photometric_scales_sparse_positive_pixels_gets_zero_flux() -> None:
    """A frame with <100 positive pixels (e.g. a near-empty edge frame)
    is assigned flux=0.0 rather than a noisy 95th-percentile estimate."""
    sparse = np.zeros((32, 32), dtype=np.float32)
    sparse[0, 0] = 5.0  # single positive pixel, far under the 100px floor
    normal = np.full((32, 32), 50.0, dtype=np.float32)
    scales = estimate_photometric_scales([sparse, normal])
    assert len(scales) == 2
    # Sparse frame's flux was floored to 0.0, so it doesn't skew the median;
    # normal frame becomes the sole positive reference (scale == 1.0).
    assert scales[1] == 1.0


def test_estimate_photometric_scales_all_frames_have_no_positive_flux() -> None:
    """When every frame's 95th-percentile flux estimate is 0 (all-empty or
    all-negative arrays), scales must default to identity (1.0) rather than
    dividing by zero."""
    arrays = [np.zeros((32, 32), dtype=np.float32) for _ in range(3)]
    scales = estimate_photometric_scales(arrays)
    assert scales == [1.0, 1.0, 1.0]


def test_estimate_photometric_scales_mixed_empty_and_populated_frames() -> None:
    empty = np.zeros((32, 32), dtype=np.float32)
    populated = np.full((32, 32), 200.0, dtype=np.float32)
    scales = estimate_photometric_scales([empty, populated])
    assert len(scales) == 2
    assert scales[1] == 1.0  # sole positive-flux frame becomes its own reference


def test_estimate_photometric_scales_target_fluxes_ignored_when_incomplete() -> None:
    """target_fluxes with any None entry (e.g. one frame's target aperture
    fell off-field) must fall back to the whole-image heuristic rather than
    scaling on a partial target-flux list."""
    a = np.full((32, 32), 100.0, dtype=np.float32)
    b = np.full((32, 32), 200.0, dtype=np.float32)
    # Only one of two target fluxes resolved -> falls back to whole-image.
    scales = estimate_photometric_scales([a, b], target_fluxes=[50.0, None])
    assert len(scales) == 2
    assert all(s > 0 for s in scales)


def test_estimate_photometric_scales_target_fluxes_used_when_complete() -> None:
    a = np.full((32, 32), 100.0, dtype=np.float32)
    b = np.full((32, 32), 100.0, dtype=np.float32)
    scales = estimate_photometric_scales([a, b], target_fluxes=[50.0, 100.0])
    # ref = median([50, 100]) = 75; scales = [75/50, 75/100] = [1.5, 0.75]
    assert scales[0] == 1.5
    assert scales[1] == 0.75
