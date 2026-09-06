"""#414 - the tiled stack accumulator must reproduce the whole-frame result.

``combine.stack_frames`` walks the frame in ``STACK_TILE_PX`` tiles instead
of allocating full ``(n_frames, ny, nx)`` float64 cubes. The #411 per-pixel
iterative clip and the #413 mask fold are decided independently at each
``(y, x)`` along the frame axis, so a tiled run is bit-close to the
single-tile reference for *any* tile size. This checks that on a 6-frame
stack for several subdividing tile sizes, plus the 5-HDU output structure
and the streaming fallback's documented "no cross-frame clip" tradeoff.

The execution plan names tile sizes {full, 512, 128}; we use small tiles
here because ``reproject_exact`` cost (~1 s / frame at 160 px, ~18 s at
512 px) would make a 512 px reference stack far too slow for the suite.
The parity property is tile-size-independent, so {64, 48, 40} - each of
which genuinely subdivides the 160 px frame, two of them with a ragged
last tile - exercises it fully.
"""

from __future__ import annotations

import os
import tracemalloc
import zlib

import numpy as np
import pytest
from astropy.io import fits
from astropy.wcs import WCS

from saucepan_pipeline.stacking.combine import stack_frames
from saucepan_pipeline.stacking.models import FrameInfo
from saucepan_pipeline.stacking.output import save_stacked_fits

SIDE = 160
N_FRAMES = 6
FULL_TILE = 10_000  # >= SIDE -> a single tile == the pre-#414 whole-frame path


def _make_frame(idx: int, side: int = SIDE) -> FrameInfo:
    tid = f"tele-{idx:02d}"
    rng = np.random.default_rng(zlib.crc32(tid.encode()))
    data = rng.normal(100.0, 8.0, size=(side, side)).astype(np.float32)
    # Shared bright source so photometric scaling and the clip centre have
    # real signal to work on.
    data[side // 2 - 5 : side // 2 + 5, side // 2 - 5 : side // 2 + 5] += 4000.0
    # One frame carries a hot outlier blob the cross-frame clip should bite -
    # exercises the reject-count parity path.
    if idx == 2:
        data[20:24, 20:24] += 12000.0

    wcs = WCS(naxis=2)
    wcs.wcs.crpix = [side / 2.0, side / 2.0]
    # Sub-pixel per-frame pointing jitter -> footprints differ at the edges,
    # exercising the valid-mask / low-coverage / auto-crop parity.
    wcs.wcs.crval = [180.0 + 1.0e-5 * idx, 0.0 + 1.0e-5 * (idx % 2)]
    wcs.wcs.cdelt = [-1.0 / 3600.0, 1.0 / 3600.0]
    wcs.wcs.ctype = ["RA---TAN", "DEC--TAN"]

    header = fits.Header()
    header.update(wcs.to_header())
    header["SP_RA"] = 180.0
    header["SP_DEC"] = 0.0

    return FrameInfo(
        path=f"/tmp/{tid}.fits",
        telescope_id=tid,
        data=data,
        header=header,
        wcs=wcs,
        noise_adu=8.0,
        background=100.0,
        snr=60.0,
        fwhm_arcsec=3.0,
        pixel_scale_arcsec=1.0,
        exptime=30.0,
    )


@pytest.fixture(scope="module")
def frames() -> list[FrameInfo]:
    return [_make_frame(i) for i in range(N_FRAMES)]


@pytest.fixture(scope="module")
def reference(frames):
    """Single-tile stack - the whole-frame accumulator, pre-#414 behaviour."""
    return stack_frames(list(frames), tile_px=FULL_TILE)


_PROV_KEYS = (
    "telescope_id",
    "rejected",
    "reject_reason",
    "n_rejected_pixels",
    "n_masked_pixels",
    "clip_iterations",
    "weight_pct",
    "photometric_scale",
    "fwhm_weight_factor",
)


@pytest.mark.parametrize("tile_px", [64, 48, 40])
def test_tiled_matches_full_reference(frames, reference, tile_px):
    tiled = stack_frames(list(frames), tile_px=tile_px)

    assert tiled.science.shape == reference.science.shape
    assert tiled.crop_slice == reference.crop_slice
    assert tiled.n_frames == reference.n_frames
    assert tiled.n_rejected == reference.n_rejected

    for name in ("science", "weight_map", "noise_map"):
        np.testing.assert_allclose(
            getattr(tiled, name),
            getattr(reference, name),
            rtol=1e-6,
            atol=0.0,
            equal_nan=True,
            err_msg=f"{name} diverged at tile_px={tile_px}",
        )
    np.testing.assert_array_equal(tiled.coverage_map, reference.coverage_map)

    assert len(tiled.provenance) == len(reference.provenance)
    for got, exp in zip(tiled.provenance, reference.provenance):
        for key in _PROV_KEYS:
            assert got[key] == exp[key], f"provenance[{key}] at tile_px={tile_px}"


def test_clip_actually_fires_in_the_reference(reference):
    """Guard: the parity check is only meaningful if the clip rejected
    something in the reference run (the injected idx==2 outlier blob)."""
    assert sum(p["n_rejected_pixels"] for p in reference.provenance) > 0


def test_five_hdu_structure_preserved(frames, reference, tmp_path):
    ref_path = tmp_path / "ref.fits"
    tiled_path = tmp_path / "tiled.fits"
    save_stacked_fits(reference, list(frames), str(ref_path))
    save_stacked_fits(
        stack_frames(list(frames), tile_px=48), list(frames), str(tiled_path)
    )

    with fits.open(ref_path) as ref_hdul, fits.open(tiled_path) as tiled_hdul:
        assert len(ref_hdul) == len(tiled_hdul) == 5
        assert [h.name for h in tiled_hdul] == [
            "PRIMARY",
            "WEIGHT",
            "NOISE",
            "COVERAGE",
            "PROVENANCE",
        ]
        for i in range(4):
            np.testing.assert_allclose(
                np.nan_to_num(tiled_hdul[i].data),
                np.nan_to_num(ref_hdul[i].data),
                rtol=1e-6,
                atol=0.0,
            )
        for key in ("SP_NSTACK", "SP_NREJECT", "SP_BUNIT", "SP_PIPE"):
            assert tiled_hdul[0].header[key] == ref_hdul[0].header[key]
        assert list(tiled_hdul["PROVENANCE"].data["telescope_id"]) == list(
            ref_hdul["PROVENANCE"].data["telescope_id"]
        )


def test_streaming_matches_tiled_when_clip_disabled(frames):
    """With ``sigma_clip=0`` the only thing separating streaming from the
    tiled accumulator is the clip, so the two must agree bit-for-bit."""
    tiled = stack_frames(list(frames), tile_px=FULL_TILE, sigma_clip=0.0)
    streamed = stack_frames(
        list(frames), tile_px=48, sigma_clip=0.0, force_streaming=True
    )
    for name in ("science", "weight_map", "noise_map"):
        np.testing.assert_allclose(
            getattr(streamed, name),
            getattr(tiled, name),
            rtol=1e-6,
            atol=0.0,
            equal_nan=True,
            err_msg=f"{name} streaming vs tiled",
        )
    np.testing.assert_array_equal(streamed.coverage_map, tiled.coverage_map)


def test_streaming_disables_cross_frame_clip(frames):
    """Documented tradeoff: streaming mode is single-pass, so the injected
    outlier that the tiled clip rejects survives into a streamed stack."""
    tiled = stack_frames(list(frames), tile_px=48)
    streamed = stack_frames(list(frames), tile_px=48, force_streaming=True)

    assert sum(p["n_rejected_pixels"] for p in tiled.provenance) > 0
    assert sum(p["n_rejected_pixels"] for p in streamed.provenance) == 0
    assert all(p["clip_iterations"] == 0 for p in streamed.provenance)
    # Skipping the clip changes the science image: the un-rejected outlier
    # blob pulls the streamed weighted mean well away from the clipped one.
    diff = np.abs(np.nan_to_num(streamed.science) - np.nan_to_num(tiled.science))
    assert diff.max() > 100.0


def test_reprojection_working_set_honors_memory_budget(frames):
    with pytest.raises(ValueError, match="reprojection working set"):
        stack_frames(list(frames), tile_px=48, mem_budget_mb=1)


@pytest.mark.skipif(
    not os.environ.get("SP_STACK_BENCHMARK"),
    reason="benchmark, not a CI gate - set SP_STACK_BENCHMARK=1 to run",
)
def test_peak_rss_scales_with_tile_size():
    """Benchmark note (non-gating): tracemalloc peak for a tiled run is a
    fraction of the whole-frame run on a stack big enough to matter."""
    big = [_make_frame(i, side=320) for i in range(10)]

    tracemalloc.start()
    stack_frames(list(big), tile_px=FULL_TILE)
    full_peak = tracemalloc.get_traced_memory()[1]
    tracemalloc.stop()

    tracemalloc.start()
    stack_frames(list(big), tile_px=64)
    tiled_peak = tracemalloc.get_traced_memory()[1]
    tracemalloc.stop()

    print(f"\npeak RSS: full={full_peak / 1e6:.1f} MB  tile64={tiled_peak / 1e6:.1f} MB")
    assert tiled_peak < 0.7 * full_peak
