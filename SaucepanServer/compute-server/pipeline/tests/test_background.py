"""background.py — 2D background subtraction and RMS estimation.

Runs after calibration in the strict pipeline order (calibration ->
background -> quality -> ...). Output must stay non-negative (physical
floor) and remain in electrons/ADU, never rescaled.
"""

from __future__ import annotations

import numpy as np
import pytest
from saucepan_pipeline.background import get_background_rms, subtract_background


def _flat_field(value=200.0, size=64, noise=5.0, seed=0) -> np.ndarray:
    rng = np.random.default_rng(seed)
    data = np.full((size, size), value, dtype=np.float32)
    data += rng.normal(0, noise, size=(size, size)).astype(np.float32)
    return data


# --- shape / dtype validation -----------------------------------------


def test_subtract_background_rejects_1d_array() -> None:
    with pytest.raises(ValueError, match="2D"):
        subtract_background(np.zeros(10, dtype=np.float32))


def test_subtract_background_rejects_3d_array() -> None:
    with pytest.raises(ValueError, match="2D"):
        subtract_background(np.zeros((4, 4, 4), dtype=np.float32))


def test_subtract_background_converts_non_float32_dtype() -> None:
    data = np.full((64, 64), 100.0, dtype=np.float64)
    result, bg = subtract_background(data)
    assert result.dtype == np.float32
    assert bg.dtype == np.float32


def test_get_background_rms_rejects_non_2d() -> None:
    with pytest.raises(ValueError, match="2D"):
        get_background_rms(np.zeros((2, 2, 2), dtype=np.float32))


# --- box_size clamping ----------------------------------------------------


def test_subtract_background_clamps_oversized_box_size() -> None:
    data = _flat_field(size=32)
    # box_size larger than half the image should be clamped, not raise.
    result, bg_map = subtract_background(data, box_size=1000)
    assert result.shape == data.shape
    assert bg_map.shape == data.shape


def test_get_background_rms_clamps_oversized_box_size() -> None:
    data = _flat_field(size=32)
    rms = get_background_rms(data, box_size=1000)
    assert rms >= 0.0


# --- physical floor: no negative pixels after subtraction ------------------


def test_subtract_background_clips_negative_to_zero() -> None:
    data = _flat_field(value=10.0, noise=20.0, size=64)  # noisy enough to go negative
    result, bg_map = subtract_background(data)
    assert (result >= 0).all()


def test_subtract_background_never_rescales_to_unit_interval() -> None:
    """Background subtraction must not normalize to [0,1] -- electrons stay
    electrons. A bright flat field's max should remain >> 1 after subtraction
    of a comparable background (i.e. this stage doesn't touch units)."""
    data = _flat_field(value=5000.0, noise=2.0, size=64)
    result, bg_map = subtract_background(data)
    # Background ~5000 subtracted leaves near-zero residual, but bg_map itself
    # should be on the original ADU/electron scale, not [0,1].
    assert bg_map.max() > 10.0


# --- basic correctness: uniform field -> near-zero residual ----------------


def test_subtract_background_uniform_field_residual_near_zero() -> None:
    data = _flat_field(value=500.0, noise=1.0, size=64, seed=1)
    result, bg_map = subtract_background(data, box_size=16)
    assert np.median(result) < 20.0  # residual should be small relative to 500
    assert np.median(bg_map) == pytest.approx(500.0, abs=20.0)


def test_get_background_rms_returns_nonneg_scalar_for_uniform_field() -> None:
    data = _flat_field(value=300.0, noise=3.0, size=64, seed=2)
    rms = get_background_rms(data)
    assert isinstance(rms, float)
    assert rms >= 0.0


# --- empty / degenerate images ----------------------------------------------


def test_subtract_background_tiny_image_does_not_crash() -> None:
    data = np.full((4, 4), 50.0, dtype=np.float32)
    result, bg_map = subtract_background(data, box_size=50)
    assert result.shape == (4, 4)


def test_subtract_background_all_zero_image() -> None:
    data = np.zeros((32, 32), dtype=np.float32)
    result, bg_map = subtract_background(data)
    assert (result >= 0).all()
    assert np.all(np.isfinite(result))
