"""stacking/output.py — builds the final multi-extension stacked FITS
(5-HDU output per the pipeline contract: SCIENCE + WEIGHT + NOISE +
COVERAGE + PROVENANCE bintable).
"""

from __future__ import annotations

from pathlib import Path

import numpy as np
import pytest
from astropy.io import fits
from astropy.wcs import WCS
from saucepan_pipeline.stacking.models import FrameInfo, StackResult
from saucepan_pipeline.stacking.output import build_output_header, save_stacked_fits


def _tan_wcs(shape=(16, 16)) -> WCS:
    h, w = shape
    hdr = fits.Header()
    hdr["CTYPE1"] = "RA---TAN"
    hdr["CTYPE2"] = "DEC--TAN"
    hdr["CRPIX1"] = w / 2.0
    hdr["CRPIX2"] = h / 2.0
    hdr["CRVAL1"] = 100.0
    hdr["CRVAL2"] = 10.0
    hdr["CDELT1"] = -1.0 / 3600.0
    hdr["CDELT2"] = 1.0 / 3600.0
    return WCS(hdr)


def _frame(path="mem", tele="t", sp_emulator=False, bunit=None) -> FrameInfo:
    header = fits.Header()
    if bunit:
        header["SP_BUNIT"] = bunit
    return FrameInfo(
        path=path,
        telescope_id=tele,
        data=np.zeros((4, 4)),
        header=header,
        wcs=_tan_wcs(shape=(4, 4)),
        sp_emulator=sp_emulator,
    )


def _result(science, provenance=None, n_frames=1, n_rejected=0) -> StackResult:
    shape = science.shape
    return StackResult(
        science=science,
        weight_map=np.ones(shape, dtype=np.float32),
        noise_map=np.full(shape, 2.0, dtype=np.float32),
        coverage_map=np.ones(shape, dtype=np.int32),
        ref_wcs=_tan_wcs(shape=shape),
        n_frames=n_frames,
        n_rejected=n_rejected,
        provenance=provenance or [],
    )


def _provenance_entry(**overrides) -> dict:
    base = dict(
        telescope_id="t1",
        exptime=10.0,
        fwhm_arcsec=3.0,
        pixel_scale=1.0,
        noise_adu=2.0,
        snr=5.0,
        weight=1.0,
        weight_pct=50.0,
        fwhm_weight_factor=1.0,
        photometric_scale=1.0,
        rejected=False,
        reject_reason="",
        n_rejected_pixels=0,
    )
    base.update(overrides)
    return base


# --- build_output_header ---------------------------------------------------


def test_build_output_header_basic_fields() -> None:
    science = np.full((16, 16), 100.0, dtype=np.float32)
    result = _result(science, provenance=[_provenance_entry()])
    header = build_output_header(result, [_frame()])
    assert header["SP_NSTACK"] == 1
    assert header["SP_BUNIT"] == "electron"
    assert header["FRM001_ID"] == "t1"


def test_build_output_header_preserves_adu_unit() -> None:
    science = np.full((16, 16), 100.0, dtype=np.float32)
    result = _result(science, provenance=[_provenance_entry()])
    header = build_output_header(result, [_frame(bunit="adu")])
    assert header["SP_BUNIT"] == "adu"


def test_build_output_header_rejects_mixed_units() -> None:
    science = np.full((16, 16), 100.0, dtype=np.float32)
    result = _result(science, provenance=[_provenance_entry()])
    with pytest.raises(ValueError, match="mixed physical units"):
        build_output_header(result, [_frame(bunit="adu"), _frame(bunit="electron")])


def test_build_output_header_no_frames_reff_unknown() -> None:
    science = np.full((16, 16), 100.0, dtype=np.float32)
    result = _result(science)
    header = build_output_header(result, [])
    assert header["SP_REFF"] == "unknown"


def test_build_output_header_all_nan_science_skips_quality_block() -> None:
    science = np.full((16, 16), np.nan, dtype=np.float32)
    result = _result(science)
    header = build_output_header(result, [_frame()])
    assert "SP_SNR" not in header


def test_build_output_header_sets_emulator_flag_when_all_frames_synthetic() -> None:
    science = np.full((16, 16), 100.0, dtype=np.float32)
    result = _result(science, provenance=[_provenance_entry()])
    frames = [_frame(sp_emulator=True), _frame(sp_emulator=True)]
    header = build_output_header(result, frames)
    assert header["SP_EMULATOR"] == 1
    assert header["SP_TIER"] == "emulator"


def test_build_output_header_no_emulator_flag_when_mixed_frames() -> None:
    science = np.full((16, 16), 100.0, dtype=np.float32)
    result = _result(science, provenance=[_provenance_entry()])
    frames = [_frame(sp_emulator=True), _frame(sp_emulator=False)]
    header = build_output_header(result, frames)
    assert "SP_EMULATOR" not in header


def test_build_output_header_target_fields_na_without_radec() -> None:
    science = np.full((16, 16), 100.0, dtype=np.float32)
    result = _result(science, provenance=[_provenance_entry()])
    header = build_output_header(result, [_frame()])
    assert header["SP_STFLX"] == "N/A"
    assert header["SP_SNRT"] == "N/A"


# --- save_stacked_fits -------------------------------------------------------


def test_save_stacked_fits_writes_five_hdus(tmp_path: Path) -> None:
    science = np.full((16, 16), 100.0, dtype=np.float32)
    result = _result(science, provenance=[_provenance_entry()])
    out = tmp_path / "stack.fits"
    save_stacked_fits(result, [_frame()], str(out))
    with fits.open(out) as hdul:
        names = [h.name for h in hdul]
        assert names == ["PRIMARY", "WEIGHT", "NOISE", "COVERAGE", "PROVENANCE"]
        assert hdul["PROVENANCE"].data["telescope_id"][0].strip() == "t1"


def test_save_stacked_fits_empty_provenance_still_writes_bintable(tmp_path: Path) -> None:
    science = np.full((16, 16), 100.0, dtype=np.float32)
    result = _result(science, provenance=[])
    out = tmp_path / "stack.fits"
    save_stacked_fits(result, [_frame()], str(out))
    with fits.open(out) as hdul:
        assert hdul["PROVENANCE"].name == "PROVENANCE"
        assert len(hdul["PROVENANCE"].data) == 0


def test_save_stacked_fits_multiple_provenance_entries_numbered_correctly(tmp_path: Path) -> None:
    science = np.full((16, 16), 100.0, dtype=np.float32)
    prov = [_provenance_entry(telescope_id="a"), _provenance_entry(telescope_id="b")]
    result = _result(science, provenance=prov, n_frames=2)
    out = tmp_path / "stack.fits"
    save_stacked_fits(result, [_frame(tele="a"), _frame(tele="b")], str(out))
    with fits.open(out) as hdul:
        assert hdul[0].header["FRM001_ID"] == "a"
        assert hdul[0].header["FRM002_ID"] == "b"
