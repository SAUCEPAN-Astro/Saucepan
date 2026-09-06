"""calibration.py — bias/dark/flat, gain conversion, cosmic-ray removal,
calstat merging. Covers the SP_BUNIT=electron contract: normalize_image()
must be skipped whenever header['SP_BUNIT'] == 'electron' (never normalize
pixel data to [0,1] post-gain-conversion).
"""

from __future__ import annotations

from pathlib import Path

import numpy as np
import pytest
from astropy.io import fits
from saucepan_pipeline.calibration import (
    apply_calibration_steps,
    apply_gain_conversion,
    apply_normalization,
    calibrate_image,
    determine_calibration_steps,
    load_and_prepare_image,
    load_calibration_frame,
    normalize_image,
    remove_cosmic_rays,
    save_calibrated_image,
    update_calstat,
    update_header_with_calibration,
)


def _write_frame(path: Path, value: float, shape=(16, 16), **hdr_kv) -> None:
    data = np.full(shape, value, dtype=np.float32)
    hdu = fits.PrimaryHDU(data=data)
    for k, v in hdr_kv.items():
        hdu.header[k] = v
    hdu.writeto(path, overwrite=True)


# --- determine_calibration_steps / apply_calibration_steps ---------------


def test_determine_calibration_steps_all_present_and_not_applied(tmp_path: Path) -> None:
    bias = tmp_path / "bias.fits"
    dark = tmp_path / "dark.fits"
    flat = tmp_path / "flat.fits"
    for p in (bias, dark, flat):
        _write_frame(p, 1.0)
    b, d, f = determine_calibration_steps("NONE", str(bias), str(dark), str(flat))
    assert (b, d, f) == (True, True, True)


def test_determine_calibration_steps_skips_when_already_in_calstat(tmp_path: Path) -> None:
    bias = tmp_path / "bias.fits"
    _write_frame(bias, 1.0)
    b, d, f = determine_calibration_steps("B", str(bias), None, None)
    assert b is False


def test_determine_calibration_steps_skips_missing_file(tmp_path: Path) -> None:
    b, d, f = determine_calibration_steps("NONE", str(tmp_path / "missing.fits"), None, None)
    assert b is False


def test_apply_calibration_steps_subtracts_bias_dark_and_divides_flat(tmp_path: Path) -> None:
    bias = tmp_path / "bias.fits"
    dark = tmp_path / "dark.fits"
    flat = tmp_path / "flat.fits"
    _write_frame(bias, 10.0)
    _write_frame(dark, 5.0)
    _write_frame(flat, 2.0)  # normalized flat = flat/median(flat) = 1.0 everywhere
    data = np.full((16, 16), 100.0, dtype=np.float32)
    result, applied, ad, af, ab = apply_calibration_steps(
        data, "NONE", str(bias), str(dark), str(flat)
    )
    assert applied == "BDF"
    assert (ad, af, ab) == (True, True, True)
    assert result[0, 0] == pytest.approx(100.0 - 10.0 - 5.0)


def test_apply_calibration_steps_shape_mismatch_is_skipped_with_warning(tmp_path: Path) -> None:
    bias = tmp_path / "bias.fits"
    _write_frame(bias, 10.0, shape=(8, 8))
    data = np.full((16, 16), 100.0, dtype=np.float32)
    result, applied, ad, af, ab = apply_calibration_steps(data, "NONE", str(bias), None, None)
    assert "B" not in applied
    assert np.array_equal(result, data)


def test_apply_calibration_steps_dark_shape_mismatch_is_skipped_with_warning(
    tmp_path: Path,
) -> None:
    dark = tmp_path / "dark.fits"
    _write_frame(dark, 5.0, shape=(4, 4))
    data = np.full((16, 16), 100.0, dtype=np.float32)
    result, applied, ad, af, ab = apply_calibration_steps(data, "NONE", None, str(dark), None)
    assert "D" not in applied
    assert np.array_equal(result, data)


def test_apply_calibration_steps_flat_shape_mismatch_is_skipped_with_warning(
    tmp_path: Path,
) -> None:
    flat = tmp_path / "flat.fits"
    _write_frame(flat, 2.0, shape=(4, 4))
    data = np.full((16, 16), 100.0, dtype=np.float32)
    result, applied, ad, af, ab = apply_calibration_steps(data, "NONE", None, None, str(flat))
    assert "F" not in applied
    assert np.array_equal(result, data)


def test_apply_calibration_steps_flat_zero_pixels_become_unity(tmp_path: Path) -> None:
    flat = tmp_path / "flat.fits"
    flat_data = np.full((16, 16), 2.0, dtype=np.float32)
    flat_data[0, 0] = 0.0  # will become flat_norm == 0 -> set to 1.0
    hdu = fits.PrimaryHDU(data=flat_data)
    hdu.writeto(flat, overwrite=True)
    data = np.full((16, 16), 50.0, dtype=np.float32)
    result, applied, *_ = apply_calibration_steps(data, "NONE", None, None, str(flat))
    assert np.isfinite(result).all()  # no divide-by-zero -> inf/nan
    assert "F" in applied


def test_apply_calibration_steps_already_applied_flags_logged_no_reapply() -> None:
    data = np.full((4, 4), 1.0, dtype=np.float32)
    result, applied, ad, af, ab = apply_calibration_steps(data, "BDF", None, None, None)
    assert applied == ""
    assert np.array_equal(result, data)


# --- gain conversion: SP_BUNIT contract ------------------------------------


def test_apply_gain_conversion_with_explicit_gain_sets_electron() -> None:
    data = np.full((4, 4), 10.0, dtype=np.float32)
    header = fits.Header()
    out_data, out_header = apply_gain_conversion(data, header, "unused.fits", {"gain": 2.0})
    assert out_header["SP_BUNIT"] == "electron"
    assert out_data[0, 0] == pytest.approx(20.0)


def test_apply_gain_conversion_no_gain_sets_adu() -> None:
    data = np.full((4, 4), 10.0, dtype=np.float32)
    header = fits.Header()
    out_data, out_header = apply_gain_conversion(data, header, "unused.fits", {})
    assert out_header["SP_BUNIT"] == "adu"
    assert np.array_equal(out_data, data)


def test_apply_gain_conversion_reads_gain_from_fits_header(tmp_path: Path) -> None:
    p = tmp_path / "f.fits"
    _write_frame(p, 5.0, SP_GAIN=1.5)
    data = np.full((16, 16), 5.0, dtype=np.float32)
    header = fits.Header()
    out_data, out_header = apply_gain_conversion(data, header, str(p), {})
    assert out_header["SP_BUNIT"] == "electron"
    assert out_data[0, 0] == pytest.approx(7.5)


def test_apply_gain_conversion_negative_gain_treated_as_absent() -> None:
    data = np.full((4, 4), 10.0, dtype=np.float32)
    header = fits.Header()
    out_data, out_header = apply_gain_conversion(data, header, "unused.fits", {"gain": -1.0})
    assert out_header["SP_BUNIT"] == "adu"


def test_apply_gain_conversion_bad_fits_path_falls_back_gracefully() -> None:
    data = np.full((4, 4), 10.0, dtype=np.float32)
    header = fits.Header()
    out_data, out_header = apply_gain_conversion(data, header, "/no/such/file.fits", {})
    assert out_header["SP_BUNIT"] == "adu"


# --- normalize_image / apply_normalization: never [0,1] when electron -----


def test_apply_normalization_skipped_when_bunit_electron() -> None:
    """This is the hard contract check: data in electrons must never be
    rescaled to [0,1], even if the caller asked for normalize=True."""
    data = np.array([[1.0, 500.0], [1000.0, 2000.0]], dtype=np.float32)
    header = {"SP_BUNIT": "electron"}
    result = apply_normalization(data, header, {"normalize": True})
    assert np.array_equal(result, data)
    assert result.max() > 1.0  # confirms it was NOT rescaled to [0,1]


def test_apply_normalization_applies_when_bunit_adu_and_requested() -> None:
    data = np.linspace(0, 1000, 100).reshape(10, 10).astype(np.float32)
    header = {"SP_BUNIT": "adu"}
    result = apply_normalization(data, header, {"normalize": True})
    assert result.min() >= 0.0
    assert result.max() <= 1.0 + 1e-6


def test_apply_normalization_noop_when_not_requested() -> None:
    data = np.full((4, 4), 55.0, dtype=np.float32)
    header = {"SP_BUNIT": "adu"}
    result = apply_normalization(data, header, {})
    assert np.array_equal(result, data)


def test_normalize_image_empty_array_returns_as_is() -> None:
    data = np.zeros((0, 0), dtype=np.float32)
    result = normalize_image(data)
    assert result.shape == (0, 0)


def test_normalize_image_clips_outliers_to_0_1() -> None:
    data = (
        np.concatenate([np.zeros(1), np.linspace(0, 100, 98), np.array([1e6])])
        .reshape(10, 10)
        .astype(np.float32)
    )
    result = normalize_image(data)
    assert result.min() >= 0.0
    assert result.max() <= 1.0


# --- update_calstat ---------------------------------------------------------


def test_update_calstat_merges_new_steps_with_none() -> None:
    assert update_calstat("NONE", "BD") == "BD"


def test_update_calstat_merges_with_existing() -> None:
    assert update_calstat("B", "D") == "BD"


def test_update_calstat_no_new_steps_returns_unchanged() -> None:
    assert update_calstat("BDF", "") == "BDF"


def test_update_calstat_dedupes_repeated_letters() -> None:
    result = update_calstat("B", "B")
    assert result == "B"


# --- remove_cosmic_rays (L.A.Cosmic via astroscrappy, #441) ----------------


def _noisy_field(shape=(64, 64), level=200.0, seed=0) -> np.ndarray:
    """Flat sky with realistic Poisson-ish noise, no sources."""
    rng = np.random.default_rng(seed)
    return rng.normal(level, np.sqrt(level), shape).astype(np.float32)


def _gaussian_star(shape=(64, 64), center=(32, 32), fwhm=3.0, peak=6000.0) -> np.ndarray:
    yy, xx = np.mgrid[0 : shape[0], 0 : shape[1]]
    sigma = fwhm / 2.3548
    r2 = (xx - center[1]) ** 2 + (yy - center[0]) ** 2
    return (peak * np.exp(-r2 / (2 * sigma**2))).astype(np.float32)


def test_remove_cosmic_rays_returns_tuple_of_clean_and_mask() -> None:
    data = _noisy_field()
    cleaned, mask = remove_cosmic_rays(data)
    assert cleaned.shape == data.shape
    assert mask.shape == data.shape
    assert mask.dtype == bool


def test_remove_cosmic_rays_empty_array() -> None:
    data = np.zeros((0, 0), dtype=np.float32)
    cleaned, mask = remove_cosmic_rays(data)
    assert cleaned.shape == (0, 0)
    assert mask.shape == (0, 0)
    assert mask.dtype == bool


def test_remove_cosmic_rays_clean_input_empty_mask_unchanged() -> None:
    """A source-free noisy field has no cosmic rays: empty mask, and the
    cleaned pixels match the input where nothing was flagged."""
    data = _noisy_field(seed=7)
    original = data.copy()
    cleaned, mask = remove_cosmic_rays(data)
    assert not mask.any()
    assert np.array_equal(data, original)  # input untouched (#456)
    assert cleaned is not data
    np.testing.assert_array_equal(cleaned[~mask], data[~mask])


def test_remove_cosmic_rays_detects_and_masks_small_spike() -> None:
    """A synthetic 1-2px cosmic-ray spike is flagged, the returned mask marks
    exactly the injected pixels, and those pixels are repaired downward."""
    data = _noisy_field(seed=1)
    injected = np.zeros(data.shape, dtype=bool)
    for y, x in [(12, 20), (40, 50), (41, 50)]:  # one single-px, one 2-px hit
        data[y, x] += 4000.0
        injected[y, x] = True

    cleaned, mask = remove_cosmic_rays(data, sigma=5.0)

    assert mask[injected].all()  # every injected pixel detected
    # L.A.Cosmic may grow the mask onto immediate neighbours (sigfrac), but
    # detections stay confined to a 1-px halo of the injected hits and the
    # count stays tiny — no wholesale field rejection.
    halo = np.zeros(data.shape, dtype=bool)
    ys, xs = np.where(injected)
    for y, x in zip(ys, xs):
        halo[max(y - 1, 0) : y + 2, max(x - 1, 0) : x + 2] = True
    assert (mask & ~halo).sum() == 0
    assert mask.sum() <= injected.sum() + 3
    assert (cleaned[injected] < data[injected] - 1000.0).all()  # repaired


def test_remove_cosmic_rays_spares_gaussian_star_core() -> None:
    """A PSF-shaped stellar core must NOT be flagged as a cosmic ray."""
    data = _noisy_field(seed=2) + _gaussian_star(fwhm=3.0, peak=8000.0)
    cleaned, mask = remove_cosmic_rays(data, sigma=5.0)
    assert not mask[28:37, 28:37].any()  # star region clean
    np.testing.assert_allclose(cleaned[30:35, 30:35], data[30:35, 30:35])


def test_remove_cosmic_rays_does_not_mutate_input() -> None:
    """#456: pure — the caller's array is intact and the result is distinct."""
    data = _noisy_field(seed=3)
    data[5, 5] += 5000.0  # cosmic ray
    original = data.copy()
    cleaned, mask = remove_cosmic_rays(data, sigma=5.0)
    assert np.array_equal(data, original)  # input untouched
    assert cleaned is not data
    assert mask[5, 5]  # clip applied to the copy


def test_remove_cosmic_rays_reads_gain_from_header() -> None:
    """Header gain/readnoise/satlevel are accepted (noise-model only) and the
    call still returns a well-formed (clean, mask) pair."""
    data = _noisy_field(seed=4)
    data[10, 10] += 6000.0
    header = {"SP_GAIN": 2.0, "SP_RDNOISE": 4.0, "SP_SATURATE": 60000.0, "SP_BUNIT": "electron"}
    cleaned, mask = remove_cosmic_rays(data, sigma=5.0, header=header)
    assert mask[10, 10]
    assert cleaned.shape == data.shape


# --- load_calibration_frame: NaN/Inf handling -------------------------------


def test_load_calibration_frame_replaces_nan_and_inf(tmp_path: Path) -> None:
    p = tmp_path / "cal.fits"
    data = np.array([[np.nan, np.inf], [-np.inf, 5.0]], dtype=np.float32)
    fits.PrimaryHDU(data=data).writeto(p, overwrite=True)
    result = load_calibration_frame(str(p))
    assert np.isfinite(result).all()
    assert result[1, 1] == pytest.approx(5.0)


# --- load_and_prepare_image / save_calibrated_image -------------------------


def test_load_and_prepare_image_returns_float32_data_and_header(tmp_path: Path) -> None:
    p = tmp_path / "f.fits"
    _write_frame(p, 42.0, SP_TELE="t1")
    data, header = load_and_prepare_image(str(p))
    assert data.dtype == np.float32
    assert header["SP_TELE"] == "t1"


def test_save_calibrated_image_writes_readable_fits(tmp_path: Path) -> None:
    out = tmp_path / "out.fits"
    data = np.full((4, 4), 3.0, dtype=np.float32)
    header = fits.Header()
    header["SP_TELE"] = "t"
    save_calibrated_image(data, header, str(out))
    with fits.open(out) as hdul:
        assert np.array_equal(hdul[0].data, data)
        assert hdul[0].header["SP_TELE"] == "t"


# --- update_header_with_calibration -----------------------------------------


def test_update_header_with_calibration_sets_expected_keys() -> None:
    header = fits.Header()
    update_header_with_calibration(header, "V", True, False, True, "BF")
    assert header["CALIBRAT"] is True
    assert header["CAL_FILT"] == "V"
    assert header["CAL_DARK"] is True
    assert header["CAL_FLAT"] is False
    assert header["CAL_BIAS"] is True
    assert header["SP_CALSTAT"] == "BF"


# --- calibrate_image: end-to-end orchestrator -------------------------------


def test_calibrate_image_end_to_end_no_cal_frames(tmp_path: Path) -> None:
    inp = tmp_path / "in.fits"
    out = tmp_path / "out.fits"
    _write_frame(inp, 100.0, SP_GAIN=2.0)
    result_path = calibrate_image(str(inp), str(out), "V", config={"remove_cosmic_rays": False})
    assert result_path == str(out)
    with fits.open(out) as hdul:
        assert hdul[0].header["SP_BUNIT"] == "electron"
        assert hdul[0].header["SP_CALSTAT"] == "NONE"
        assert hdul[0].data[0, 0] == pytest.approx(200.0)


def test_calibrate_image_with_bias_dark_flat(tmp_path: Path) -> None:
    inp = tmp_path / "in.fits"
    bias = tmp_path / "bias.fits"
    dark = tmp_path / "dark.fits"
    flat = tmp_path / "flat.fits"
    out = tmp_path / "out.fits"
    _write_frame(inp, 1000.0, SP_GAIN=1.0)
    _write_frame(bias, 100.0)
    _write_frame(dark, 50.0)
    _write_frame(flat, 1.0)
    calibrate_image(
        str(inp),
        str(out),
        "V",
        dark_path=str(dark),
        flat_path=str(flat),
        bias_path=str(bias),
        config={"remove_cosmic_rays": False, "gain": 1.0},
    )
    with fits.open(out) as hdul:
        assert hdul[0].header["SP_CALSTAT"] == "BDF"
        assert hdul[0].data[0, 0] == pytest.approx(1000.0 - 100.0 - 50.0)


def test_calibrate_image_default_cosmic_ray_removal_is_off(tmp_path: Path) -> None:
    """#456: calibrate_image()'s default for 'remove_cosmic_rays' is now False
    (opt-in), matching driver._calibrate_in_memory. With no config the cosmic
    ray must survive untouched."""
    inp = tmp_path / "in.fits"
    out = tmp_path / "out.fits"
    data = np.full((16, 16), 100.0, dtype=np.float32)
    data[8, 8] = 1e7
    hdu = fits.PrimaryHDU(data=data)
    hdu.header["SP_GAIN"] = 1.0
    hdu.writeto(inp, overwrite=True)
    calibrate_image(str(inp), str(out), "V")  # no config -> default False
    with fits.open(out) as hdul:
        assert hdul[0].data[8, 8] == pytest.approx(1e7 * 1.0, rel=1e-3)


def test_calibrate_image_removes_cosmic_rays_when_explicitly_enabled(tmp_path: Path) -> None:
    """#456: opt-in still works — config={'remove_cosmic_rays': True} scrubs
    the injected cosmic ray."""
    inp = tmp_path / "in.fits"
    out = tmp_path / "out.fits"
    data = np.full((16, 16), 100.0, dtype=np.float32)
    data[8, 8] = 1e7
    hdu = fits.PrimaryHDU(data=data)
    hdu.header["SP_GAIN"] = 1.0
    hdu.writeto(inp, overwrite=True)
    calibrate_image(str(inp), str(out), "V", config={"remove_cosmic_rays": True, "gain": 1.0})
    with fits.open(out) as hdul:
        assert hdul[0].data[8, 8] < 1e6


def test_calibrate_image_propagates_exception_on_missing_input(tmp_path: Path) -> None:
    with pytest.raises(Exception):
        calibrate_image(str(tmp_path / "nope.fits"), str(tmp_path / "out.fits"), "V")
