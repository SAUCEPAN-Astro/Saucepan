"""Stack must not trust forged SP_* quality headers (#292)."""

from __future__ import annotations

from pathlib import Path

import numpy as np
import pytest
from astropy.io import fits
from saucepan_pipeline.stacking.combine import stack_frames
from saucepan_pipeline.stacking.frames import load_frame


def _write_frame(
    path: Path,
    *,
    seed: int = 0,
    forged: dict | None = None,
) -> None:
    rng = np.random.default_rng(seed)
    data = rng.normal(100.0, 5.0, size=(64, 64)).astype(np.float32)
    # Shared bright star so SNR measurement is stable across copies.
    data[30:34, 30:34] += 500.0
    hdu = fits.PrimaryHDU(data=data)
    hdr = hdu.header
    hdr["SP_RA"] = 180.0
    hdr["SP_DEC"] = 0.0
    hdr["SP_TELE"] = "test-tele"
    hdr["SP_EXPTIME"] = 10.0
    hdr["SP_PIXSCALE"] = 1.0
    hdr["CTYPE1"] = "RA---TAN"
    hdr["CTYPE2"] = "DEC--TAN"
    hdr["CRPIX1"] = 32.0
    hdr["CRPIX2"] = 32.0
    hdr["CRVAL1"] = 180.0
    hdr["CRVAL2"] = 0.0
    hdr["CDELT1"] = -1.0 / 3600.0
    hdr["CDELT2"] = 1.0 / 3600.0
    if forged:
        for key, value in forged.items():
            hdr[key] = value
    hdu.writeto(path, overwrite=True)


def test_load_frame_ignores_forged_quality_headers(tmp_path: Path) -> None:
    honest = tmp_path / "honest.fits"
    forged = tmp_path / "forged.fits"
    _write_frame(honest, seed=42)
    _write_frame(
        forged,
        seed=42,
        forged={
            "SP_BGMD": 1.0,
            "SP_BGNOI": 1e-6,
            "SP_SNR": 1e9,
            "SP_FWHM": 0.001,
            "SEEING": 0.001,
            "SP_SPX": 0,
        },
    )

    a = load_frame(str(honest))
    b = load_frame(str(forged))

    assert a.noise_adu == pytest.approx(b.noise_adu, rel=1e-6)
    assert a.background == pytest.approx(b.background, rel=1e-6)
    assert a.snr == pytest.approx(b.snr, rel=1e-6)
    # Measured FWHM must match across identical pixels; forged header ignored.
    assert a.fwhm_arcsec == pytest.approx(b.fwhm_arcsec, rel=1e-6)
    assert b.fwhm_arcsec != 0.001
    # Forged tiny noise must not be adopted.
    assert b.noise_adu > 0.1


def test_forged_headers_do_not_change_stack_weights(tmp_path: Path) -> None:
    path_a = tmp_path / "a.fits"
    path_b = tmp_path / "b.fits"
    _write_frame(path_a, seed=7)
    _write_frame(
        path_b,
        seed=7,
        forged={
            "SP_BGNOI": 1e-6,
            "SP_BGMD": 0.0,
            "SP_SNR": 1e12,
            "SP_FWHM": 0.001,
        },
    )

    frames = [load_frame(str(path_a)), load_frame(str(path_b))]
    result = stack_frames(
        frames,
        auto_crop=False,
        sigma_clip=0.0,
        weight_by_fwhm=True,
        photometric_scale=False,
    )

    weights = [p["weight"] for p in result.provenance]
    assert weights[0] == pytest.approx(weights[1], rel=1e-9)
    assert result.provenance[0]["noise_adu"] == pytest.approx(
        result.provenance[1]["noise_adu"], rel=1e-6
    )
    assert result.provenance[0]["fwhm_arcsec"] == pytest.approx(
        result.provenance[1]["fwhm_arcsec"], rel=1e-6
    )
    assert result.provenance[0]["fwhm_arcsec"] != 0.001
    assert result.provenance[1]["fwhm_arcsec"] != 0.001
