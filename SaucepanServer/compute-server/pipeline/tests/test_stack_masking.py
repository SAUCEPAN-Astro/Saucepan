"""#413 - saturated / bad-pixel-map / cosmic-ray pixels are dropped to
weight 0 in ``stacking/combine.py`` *before* the #411 per-pixel sigma-clip,
so their extreme values never bias the cross-frame median and never smear
into the stacked SCIENCE plane.

Pins:
  * a saturated stellar core contributes 0 weight there, finite in the wings;
  * a bad-pixel-map column shared by every frame is excluded everywhere and
    that column comes out NaN in SCIENCE (masked in all frames);
  * a single-frame cosmic-ray hit is masked, not median-smeared, into the
    stack (the surviving frames set the value there);
  * a pixel masked in every frame -> NaN in SCIENCE.
"""

from __future__ import annotations

import numpy as np
from astropy.io import fits
from astropy.wcs import WCS
from saucepan_pipeline.stacking.combine import stack_frames
from saucepan_pipeline.stacking.frames import load_frame
from saucepan_pipeline.stacking.models import FrameInfo

SHAPE = (48, 48)
CY, CX = 24, 24


def _wcs() -> WCS:
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


def _star(amp: float = 800.0, sigma: float = 2.5) -> np.ndarray:
    yy, xx = np.mgrid[0 : SHAPE[0], 0 : SHAPE[1]]
    return amp * np.exp(-((yy - CY) ** 2 + (xx - CX) ** 2) / (2.0 * sigma**2))


def _base() -> np.ndarray:
    return np.full(SHAPE, 100.0, dtype=np.float64) + _star()


def _frame(
    data: np.ndarray,
    tele: str,
    mask: np.ndarray | None = None,
    sources: list[str] | None = None,
    noise_adu: float = 10.0,
) -> FrameInfo:
    wcs = _wcs()
    hdr = fits.Header()
    hdr.update(wcs.to_header())
    return FrameInfo(
        path=f"mem:{tele}",
        telescope_id=tele,
        data=np.asarray(data, dtype=np.float32),
        header=hdr,
        wcs=wcs,
        noise_adu=noise_adu,
        background=100.0,
        snr=50.0,
        fwhm_arcsec=3.0,
        pixel_scale_arcsec=1.0,
        exptime=30.0,
        mask=mask,
        mask_sources=sources or [],
    )


def _stack(frames, **kw):
    defaults = dict(weight_by_fwhm=False, photometric_scale=False, auto_crop=False)
    defaults.update(kw)
    return stack_frames(frames, **defaults)


def test_saturated_core_zero_weight_there_finite_in_wings() -> None:
    """One frame's saturated 3x3 core is excluded from that frame; the core
    pixel still stacks (other frames cover it) and the wings are untouched."""
    sat = np.zeros(SHAPE, dtype=bool)
    sat[CY - 1 : CY + 2, CX - 1 : CX + 2] = True

    f0 = _frame(_base(), "sat", mask=sat, sources=["saturation"])
    f1 = _frame(_base(), "clean-1")
    f2 = _frame(_base(), "clean-2")
    res = _stack([f0, f1, f2], sigma_clip=0.0)

    prov = res.provenance[0]
    assert prov["n_masked_pixels"] == 9
    assert prov["mask_sources"] == ["saturation"]

    # Core still resolved by the two clean frames, wings finite everywhere.
    assert np.isfinite(res.science[CY, CX])
    assert np.isfinite(res.science[CY + 6, CX])
    # Coverage drops by exactly one frame inside the saturated core.
    assert res.coverage_map[CY, CX] == 2
    assert res.coverage_map[CY + 6, CX] == 3


def test_saturated_pixel_value_not_pulled_toward_saturation() -> None:
    """The masked frame carries a huge value in its core; without masking the
    weighted mean there would be dragged well above the true ~900."""
    hot = _base()
    hot[CY, CX] = 60000.0
    sat = np.zeros(SHAPE, dtype=bool)
    sat[CY, CX] = True

    masked = _stack(
        [
            _frame(hot, "hot", mask=sat, sources=["saturation"]),
            _frame(_base(), "c1"),
            _frame(_base(), "c2"),
        ],
        sigma_clip=0.0,
    )
    truth = _base()[CY, CX]
    assert abs(masked.science[CY, CX] - truth) < 5.0


def test_bad_pixel_column_shared_by_all_frames_is_nan() -> None:
    bpm = np.zeros(SHAPE, dtype=bool)
    bpm[:, 10] = True
    frames = [
        _frame(_base(), f"t{i}", mask=bpm.copy(), sources=["bad_pixel_map"])
        for i in range(3)
    ]
    res = _stack(frames, sigma_clip=0.0)

    assert np.all(np.isnan(res.science[:, 10]))
    assert np.all(res.coverage_map[:, 10] == 0)
    for p in res.provenance:
        assert p["n_masked_pixels"] == SHAPE[0]
        assert p["mask_sources"] == ["bad_pixel_map"]
    # A neighbouring column is unaffected.
    assert np.all(np.isfinite(res.science[:, 12]))


def test_cosmic_ray_hit_masked_not_median_smeared() -> None:
    """A CR spike in one frame at an off-core pixel: with the pixel masked the
    stack value equals the clean frames' level, not a blend with the spike."""
    py, px = 8, 40
    cr = _base()
    cr[py, px] = 50000.0
    crmask = np.zeros(SHAPE, dtype=bool)
    crmask[py, px] = True

    with_mask = _stack(
        [
            _frame(cr, "cr", mask=crmask, sources=["cosmic_ray"]),
            _frame(_base(), "c1"),
            _frame(_base(), "c2"),
        ],
        sigma_clip=0.0,
    )
    truth = _base()[py, px]
    assert abs(with_mask.science[py, px] - truth) < 1.0
    assert with_mask.coverage_map[py, px] == 2
    assert with_mask.provenance[0]["n_masked_pixels"] == 1
    assert with_mask.provenance[0]["mask_sources"] == ["cosmic_ray"]


def test_pixel_masked_in_every_frame_is_nan_in_science() -> None:
    py, px = 30, 5
    m = np.zeros(SHAPE, dtype=bool)
    m[py, px] = True
    frames = [
        _frame(_base(), f"t{i}", mask=m.copy(), sources=["saturation"])
        for i in range(3)
    ]
    res = _stack(frames, sigma_clip=0.0)
    assert np.isnan(res.science[py, px])
    assert res.coverage_map[py, px] == 0
    assert np.isfinite(res.science[py + 1, px])


def _write_fits(path, data, **hdr_kv) -> None:
    hdu = fits.PrimaryHDU(data=np.asarray(data, dtype=np.float32))
    for k, v in hdr_kv.items():
        hdu.header[k] = v
    hdu.writeto(path, overwrite=True)


def test_load_frame_masks_saturated_pixels(tmp_path) -> None:
    data = np.full(SHAPE, 300.0, dtype=np.float32)
    data[5, 5] = 65535.0
    data[6, 6] = 70000.0
    p = tmp_path / "sat.fits"
    _write_fits(p, data, SP_SATURATE=60000.0)

    frame = load_frame(str(p))
    assert frame.mask is not None
    assert frame.mask[5, 5] and frame.mask[6, 6]
    assert not frame.mask[0, 0]
    assert "saturation" in frame.mask_sources


def test_load_frame_no_saturation_leaves_mask_none(tmp_path) -> None:
    p = tmp_path / "clean.fits"
    _write_fits(p, np.full(SHAPE, 300.0, dtype=np.float32))
    frame = load_frame(str(p))
    assert frame.mask is None
    assert frame.mask_sources == []


def test_load_frame_reads_bad_pixel_map_sidecar(tmp_path) -> None:
    bpm = np.zeros(SHAPE, dtype=np.int16)
    bpm[:, 7] = 1
    bpm_path = tmp_path / "bpm.fits"
    fits.PrimaryHDU(data=bpm).writeto(bpm_path, overwrite=True)

    p = tmp_path / "frame.fits"
    _write_fits(p, np.full(SHAPE, 300.0, dtype=np.float32), SP_BPMASK="bpm.fits")

    frame = load_frame(str(p))
    assert frame.mask is not None
    assert np.all(frame.mask[:, 7])
    assert "bad_pixel_map" in frame.mask_sources


def test_load_frame_ignores_bad_pixel_map_outside_frame_directory(tmp_path) -> None:
    outside = tmp_path.parent / "outside.fits"
    fits.PrimaryHDU(data=np.ones(SHAPE, dtype=np.int16)).writeto(outside, overwrite=True)

    frame_path = tmp_path / "frame.fits"
    _write_fits(
        frame_path,
        np.full(SHAPE, 300.0, dtype=np.float32),
        SP_BPMASK=str(outside),
    )

    frame = load_frame(str(frame_path))
    assert frame.mask is None


def test_no_mask_is_a_noop() -> None:
    """frames with mask=None stack exactly as before (regression guard)."""
    frames = [_frame(_base(), f"t{i}") for i in range(3)]
    res = _stack(frames, sigma_clip=0.0)
    assert np.all(np.isfinite(res.science))
    for p in res.provenance:
        assert p["n_masked_pixels"] == 0
        assert p["mask_sources"] == []
