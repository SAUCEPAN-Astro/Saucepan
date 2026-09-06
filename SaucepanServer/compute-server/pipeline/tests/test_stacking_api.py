"""stacking/api.py — high-level stack_fits_files() entry point, Lambda
handler, and CLI. stack_fits_files() delegates to driver.run_stack_pipeline
(calibration -> background -> quality -> psf_match -> reproject -> stack).
"""

from __future__ import annotations

import json
import sys
from pathlib import Path
from unittest import mock

import numpy as np
import pytest
from astropy.io import fits
from saucepan_pipeline.stacking.api import handler, main, stack_fits_files


def _write_star_field(path: Path, *, seed=0, tele="t") -> None:
    rng = np.random.default_rng(seed)
    size = 48
    data = np.full((size, size), 100.0, dtype=np.float32)
    data += rng.normal(0, 2.0, size=(size, size)).astype(np.float32)
    sigma = 3.0 / 2.355
    yy, xx = np.mgrid[:size, :size]
    for y, x, amp in ((16, 16, 500.0), (32, 32, 600.0), (16, 32, 450.0)):
        data += amp * np.exp(-(((xx - x) ** 2 + (yy - y) ** 2) / (2 * sigma**2)))
    hdr = fits.Header()
    hdr["SP_RA"] = 100.0
    hdr["SP_DEC"] = 10.0
    hdr["SP_TELE"] = tele
    hdr["SP_EXPTIME"] = 10.0
    hdr["SP_PIXSCALE"] = 1.0
    hdr["SP_GAIN"] = 1.0
    hdr["SP_CALSTAT"] = "NONE"
    hdr["SP_FILTER"] = "V"
    hdr["CTYPE1"] = "RA---TAN"
    hdr["CTYPE2"] = "DEC--TAN"
    hdr["CRPIX1"] = size / 2.0
    hdr["CRPIX2"] = size / 2.0
    hdr["CRVAL1"] = 100.0
    hdr["CRVAL2"] = 10.0
    hdr["CDELT1"] = -1.0 / 3600.0
    hdr["CDELT2"] = 1.0 / 3600.0
    fits.PrimaryHDU(data=data.astype(np.float32), header=hdr).writeto(path, overwrite=True)


# --- stack_fits_files: delegates to driver ----------------------------------


def test_stack_fits_files_delegates_to_run_stack_pipeline() -> None:
    with mock.patch(
        "saucepan_pipeline.driver.run_stack_pipeline", return_value={"output": "x"}
    ) as run_mock:
        result = stack_fits_files(["a.fits", "b.fits"], "out.fits", max_psf_fwhm=5.0)
    run_mock.assert_called_once()
    args, kwargs = run_mock.call_args
    assert args[0] == ["a.fits", "b.fits"]
    assert args[1] == "out.fits"
    assert kwargs["max_psf_fwhm"] == 5.0
    assert result == {"output": "x"}


def test_stack_fits_files_end_to_end(tmp_path: Path) -> None:
    paths = []
    for i in range(2):
        p = tmp_path / f"f{i}.fits"
        _write_star_field(p, seed=i, tele=f"T{i}")
        paths.append(str(p))
    out = tmp_path / "stack.fits"
    summary = stack_fits_files(
        paths,
        str(out),
        sigma_clip=0.0,
        auto_crop=False,
        calibration_config={"remove_cosmic_rays": False, "gain": 1.0},
    )
    assert out.exists()
    assert summary["n_frames_used"] >= 1


# --- Lambda handler ------------------------------------------------------------


def test_handler_unknown_action_returns_400() -> None:
    result = handler({"action": "nope"})
    assert result["statusCode"] == 400


def test_handler_stack_end_to_end(tmp_path: Path) -> None:
    paths = []
    for i in range(2):
        p = tmp_path / f"f{i}.fits"
        _write_star_field(p, seed=i, tele=f"T{i}")
        paths.append(str(p))
    out = tmp_path / "stack.fits"
    payload = {
        "action": "stack",
        "frames": [{"path": p, "telescope_id": f"T{i}"} for i, p in enumerate(paths)],
        "output_path": str(out),
        "config": {"sigma_clip": 0.0, "auto_crop": False},
    }
    result = handler(payload)
    assert result["statusCode"] == 200
    assert "n_frames_used" in result["body"]


def test_handler_default_action_is_stack() -> None:
    """Missing 'action' key defaults to 'stack', not a 400 — but with no
    frames it should fail internally and be reported as a 500."""
    result = handler({"frames": [], "output_path": "/tmp/x.fits"})
    assert result["statusCode"] == 500


def test_handler_exception_from_bad_frame_paths_returns_500() -> None:
    result = handler(
        {
            "action": "stack",
            "frames": [{"path": "/no/such/file.fits"}],
            "output_path": "/tmp/out.fits",
        }
    )
    assert result["statusCode"] == 500
    assert "error" in result["body"]


# --- CLI main() ----------------------------------------------------------------


def test_main_cli_runs_and_prints_json(tmp_path: Path, monkeypatch, capsys) -> None:
    paths = []
    for i in range(2):
        p = tmp_path / f"f{i}.fits"
        _write_star_field(p, seed=i, tele=f"T{i}")
        paths.append(str(p))
    out = tmp_path / "stack.fits"
    argv = [
        "stack",
        "--frames",
        *paths,
        "--output",
        str(out),
        "--sigma-clip",
        "0.0",
        "--no-crop",
    ]
    monkeypatch.setattr(sys, "argv", argv)
    main()
    captured = capsys.readouterr()
    parsed = json.loads(captured.out)
    assert "n_frames_used" in parsed
