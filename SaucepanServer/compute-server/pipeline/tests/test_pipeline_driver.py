"""Pipeline driver stage wiring + FWHM + photometric scaling (#408/#409/#410)."""

from __future__ import annotations

from pathlib import Path

import numpy as np
import pytest
from astropy.io import fits
import saucepan_pipeline.calibration as calibration
from saucepan_pipeline.driver import STAGE_ORDER, prepare_frames_for_stack, run_stack_pipeline
from saucepan_pipeline.quality import assess_quality, estimate_fwhm
from saucepan_pipeline.stacking.combine import estimate_photometric_scales, stack_frames
from saucepan_pipeline.stacking.frames import load_frame


def _gaussian_star_field(
    size: int = 128,
    *,
    fwhm_px: float = 3.0,
    background: float = 100.0,
    scale: float = 1.0,
    seed: int = 0,
    pixel_scale: float = 1.0,
) -> tuple[np.ndarray, fits.Header]:
    rng = np.random.default_rng(seed)
    data = np.full((size, size), background, dtype=np.float32)
    data += rng.normal(0.0, 2.0, size=(size, size)).astype(np.float32)
    sigma = fwhm_px / 2.355
    yy, xx = np.mgrid[:size, :size]
    for y, x, amp in (
        (32, 32, 800.0),
        (32, 96, 700.0),
        (96, 32, 900.0),
        (96, 96, 750.0),
        (64, 64, 1000.0),
        (48, 80, 650.0),
    ):
        star = amp * np.exp(-((xx - x) ** 2 + (yy - y) ** 2) / (2 * sigma**2))
        data += (star * scale).astype(np.float32)

    hdr = fits.Header()
    hdr["SP_RA"] = 180.0
    hdr["SP_DEC"] = 0.0
    hdr["SP_TELE"] = "test-tele"
    hdr["SP_EXPTIME"] = 10.0
    hdr["SP_PIXSCALE"] = pixel_scale
    hdr["SP_GAIN"] = 1.0
    hdr["SP_CALSTAT"] = "NONE"
    hdr["SP_FILTER"] = "V"
    hdr["CTYPE1"] = "RA---TAN"
    hdr["CTYPE2"] = "DEC--TAN"
    hdr["CRPIX1"] = size / 2.0
    hdr["CRPIX2"] = size / 2.0
    hdr["CRVAL1"] = 180.0
    hdr["CRVAL2"] = 0.0
    hdr["CDELT1"] = -pixel_scale / 3600.0
    hdr["CDELT2"] = pixel_scale / 3600.0
    return data, hdr


def _write_star_fits(path: Path, **kwargs) -> None:
    data, hdr = _gaussian_star_field(**kwargs)
    fits.PrimaryHDU(data=data, header=hdr).writeto(path, overwrite=True)


def test_stage_order_constant() -> None:
    assert STAGE_ORDER == (
        "calibration",
        "background",
        "quality",
        "psf_match",
        "reproject",
        "stack",
    )


def test_estimate_fwhm_recovers_injected_psf() -> None:
    data, _hdr = _gaussian_star_field(fwhm_px=4.0, pixel_scale=1.0, seed=11)
    measured = estimate_fwhm(data - np.median(data), pixel_scale_arcsec=1.0)
    # Allow generous tolerance — DAOStarFinder + cutout fit on noisy field.
    assert 2.0 < measured < 7.0


def test_assess_quality_includes_fwhm() -> None:
    data, _hdr = _gaussian_star_field(fwhm_px=3.5, seed=3)
    q = assess_quality(data, pixel_scale_arcsec=1.0)
    assert "fwhm_arcsec" in q
    assert q["fwhm_arcsec"] > 0


def test_load_frame_ignores_forged_fwhm(tmp_path: Path) -> None:
    honest = tmp_path / "honest.fits"
    forged = tmp_path / "forged.fits"
    data, hdr = _gaussian_star_field(seed=42, fwhm_px=3.0)
    fits.PrimaryHDU(data=data, header=hdr).writeto(honest, overwrite=True)
    hdr2 = hdr.copy()
    hdr2["SP_FWHM"] = 0.001
    hdr2["SEEING"] = 0.001
    hdr2["SP_BGNOI"] = 1e-6
    fits.PrimaryHDU(data=data.copy(), header=hdr2).writeto(forged, overwrite=True)

    a = load_frame(str(honest))
    b = load_frame(str(forged))
    assert a.fwhm_arcsec == pytest.approx(b.fwhm_arcsec, rel=1e-6)
    assert b.fwhm_arcsec != 0.001
    assert b.noise_adu > 0.1


def test_fwhm_changes_stack_weights(tmp_path: Path) -> None:
    sharp = tmp_path / "sharp.fits"
    wide = tmp_path / "wide.fits"
    data_s, hdr_s = _gaussian_star_field(fwhm_px=2.5, seed=1, background=50.0)
    data_w, hdr_w = _gaussian_star_field(fwhm_px=6.0, seed=1, background=50.0)
    hdr_s["SP_TELE"] = "sharp"
    hdr_w["SP_TELE"] = "wide"
    fits.PrimaryHDU(data=data_s, header=hdr_s).writeto(sharp, overwrite=True)
    fits.PrimaryHDU(data=data_w, header=hdr_w).writeto(wide, overwrite=True)

    frames = [load_frame(str(sharp)), load_frame(str(wide))]
    # Force measured FWHMs for a deterministic weight check if star fit is noisy.
    frames[0].fwhm_arcsec = 2.5
    frames[1].fwhm_arcsec = 6.0
    frames[0].noise_adu = 1.0
    frames[1].noise_adu = 1.0
    result = stack_frames(
        frames,
        auto_crop=False,
        sigma_clip=0.0,
        weight_by_fwhm=True,
        photometric_scale=False,
    )
    w0 = result.provenance[0]["weight"]
    w1 = result.provenance[1]["weight"]
    assert w0 > w1
    assert w0 / w1 == pytest.approx((6.0 / 2.5) ** 2, rel=1e-6)


def test_max_psf_fwhm_rejection(tmp_path: Path) -> None:
    good = tmp_path / "good.fits"
    bad = tmp_path / "bad.fits"
    data_g, hdr_g = _gaussian_star_field(fwhm_px=2.5, seed=5)
    data_b, hdr_b = _gaussian_star_field(fwhm_px=2.5, seed=6)
    hdr_g["SP_TELE"] = "good"
    hdr_b["SP_TELE"] = "bad"
    fits.PrimaryHDU(data=data_g, header=hdr_g).writeto(good, overwrite=True)
    fits.PrimaryHDU(data=data_b, header=hdr_b).writeto(bad, overwrite=True)

    accepted, rejects, meta = prepare_frames_for_stack(
        [str(good), str(bad)],
        max_psf_fwhm=0.01,  # reject everything with any positive FWHM
        calibration_config={"remove_cosmic_rays": False, "gain": 1.0},
    )
    # Both should be quality-rejected when threshold is tiny.
    assert len(accepted) == 0
    assert len(rejects) == 2
    assert meta["stages"] == list(STAGE_ORDER)


def test_prepare_reuses_loaded_gain_header_without_changing_output(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """The science FITS is opened once, with the old calibrated result kept."""
    path = tmp_path / "frame.fits"
    data, header = _gaussian_star_field(seed=23)
    header["SP_GAIN"] = 2.0
    # This regression targets calibration/stacking I/O; omit the optional
    # target position so the unrelated target-photometry path is not involved.
    del header["SP_RA"]
    del header["SP_DEC"]
    fits.PrimaryHDU(data=data, header=header).writeto(path, overwrite=True)

    open_count = 0
    real_open = fits.open

    def counting_open(*args, **kwargs):
        nonlocal open_count
        open_count += 1
        return real_open(*args, **kwargs)

    monkeypatch.setattr(calibration.fits, "open", counting_open)
    reused, _, reused_meta = prepare_frames_for_stack(
        [str(path)],
        calibration_config={"remove_cosmic_rays": False},
    )
    assert open_count == 1  # load_frame only; calibration reuses its header

    # An explicit gain is the pre-optimization behavior's numerical reference:
    # both paths must apply the same calibration and pipeline stages.
    reference, _, reference_meta = prepare_frames_for_stack(
        [str(path)],
        calibration_config={"remove_cosmic_rays": False, "gain": 2.0},
    )

    assert open_count == 2  # one science read per prepare call; no gain reread
    assert len(reused) == len(reference) == 1
    np.testing.assert_array_equal(reused[0].data, reference[0].data)
    assert reused[0].header["SP_BUNIT"] == reference[0].header["SP_BUNIT"]
    assert reused[0].noise_adu == pytest.approx(reference[0].noise_adu)
    assert reused[0].snr == pytest.approx(reference[0].snr)
    assert reused[0].fwhm_arcsec == pytest.approx(reference[0].fwhm_arcsec)
    assert reused_meta == reference_meta


def test_run_stack_pipeline_wires_stages(tmp_path: Path) -> None:
    paths = []
    for i, fwhm in enumerate((3.0, 3.5)):
        p = tmp_path / f"f{i}.fits"
        data, hdr = _gaussian_star_field(fwhm_px=fwhm, seed=10 + i)
        hdr["SP_TELE"] = f"T{i}"
        fits.PrimaryHDU(data=data, header=hdr).writeto(p, overwrite=True)
        paths.append(str(p))
    out = tmp_path / "stack.fits"
    summary = run_stack_pipeline(
        paths,
        str(out),
        auto_crop=False,
        sigma_clip=0.0,
        max_psf_fwhm=20.0,
        photometric_scale=True,
        calibration_config={"remove_cosmic_rays": False, "gain": 1.0},
    )
    assert out.exists()
    assert summary["stages"] == list(STAGE_ORDER)
    assert summary["n_frames_used"] >= 1
    assert "provenance" in summary


def test_photometric_scales_recover_common_flux() -> None:
    base, _ = _gaussian_star_field(fwhm_px=3.0, scale=1.0, seed=0, background=0.0)
    # Background-subtracted synthetic: positive star flux only.
    base = np.clip(base - np.median(base), 0, None)
    a = base * 1.0
    b = base * 2.0
    c = base * 0.5
    scales = estimate_photometric_scales([a, b, c])
    # After scaling, 95th percentiles should match.
    scaled = [arr * s for arr, s in zip((a, b, c), scales)]
    p95 = [float(np.percentile(s[s > 0], 95)) for s in scaled]
    assert p95[0] == pytest.approx(p95[1], rel=1e-3)
    assert p95[0] == pytest.approx(p95[2], rel=1e-3)
    # Scales themselves: median flux is 1.0 → scales ≈ [1, 0.5, 2]
    assert scales[1] == pytest.approx(0.5, rel=1e-3)
    assert scales[2] == pytest.approx(2.0, rel=1e-3)
