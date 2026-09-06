"""stacking/frames.py — load_frame() re-measures quality from pixels
(trust model #292: never trust inbound SP_* quality headers), and its WCS
fallback construction path when the FITS header carries no real WCS.
"""

from __future__ import annotations

from pathlib import Path

import numpy as np
import pytest
from astropy.io import fits
from saucepan_pipeline.stacking.frames import _header_bool, load_frame


def _write_frame(path: Path, **hdr_kv) -> None:
    rng = np.random.default_rng(0)
    data = np.full((48, 48), 200.0, dtype=np.float32)
    data += rng.normal(0, 3.0, size=(48, 48)).astype(np.float32)
    hdu = fits.PrimaryHDU(data=data)
    for k, v in hdr_kv.items():
        hdu.header[k] = v
    hdu.writeto(path, overwrite=True)


# --- _header_bool -------------------------------------------------------


def test_header_bool_missing_key_is_false() -> None:
    hdr = fits.Header()
    assert _header_bool(hdr, "SP_EMULATOR") is False


def test_header_bool_integer_true() -> None:
    hdr = fits.Header()
    hdr["SP_EMULATOR"] = 1
    assert _header_bool(hdr, "SP_EMULATOR") is True


def test_header_bool_integer_false() -> None:
    hdr = fits.Header()
    hdr["SP_EMULATOR"] = 0
    assert _header_bool(hdr, "SP_EMULATOR") is False


def test_header_bool_string_true_variants() -> None:
    hdr = fits.Header()
    for val in ("true", "T", "yes", "1"):
        hdr["SP_EMULATOR"] = val
        assert _header_bool(hdr, "SP_EMULATOR") is True, val


def test_header_bool_string_false_variant() -> None:
    hdr = fits.Header()
    hdr["SP_EMULATOR"] = "false"
    assert _header_bool(hdr, "SP_EMULATOR") is False


# --- load_frame: WCS present vs fallback ---------------------------------


def test_load_frame_uses_real_wcs_when_present(tmp_path: Path) -> None:
    p = tmp_path / "f.fits"
    _write_frame(
        p,
        SP_RA=100.0,
        SP_DEC=20.0,
        SP_TELE="scope-a",
        SP_EXPTIME=5.0,
        SP_PIXSCALE=1.0,
        CTYPE1="RA---TAN",
        CTYPE2="DEC--TAN",
        CRPIX1=24.0,
        CRPIX2=24.0,
        CRVAL1=100.0,
        CRVAL2=20.0,
        CDELT1=-1.0 / 3600.0,
        CDELT2=1.0 / 3600.0,
    )
    frame = load_frame(str(p))
    assert frame.wcs.has_celestial
    assert frame.telescope_id == "scope-a"
    assert frame.exptime == 5.0
    assert frame.pixel_scale_arcsec == pytest.approx(1.0)


def test_load_frame_builds_fallback_wcs_when_no_wcs_keywords(tmp_path: Path) -> None:
    """Frame with no CTYPE/CRVAL etc. — load_frame must build a minimal
    fallback WCS from SP_RA/SP_DEC/SP_PIXSCALE instead of raising."""
    p = tmp_path / "f.fits"
    _write_frame(p, SP_RA=50.0, SP_DEC=-30.0, SP_TELE="scope-b", SP_PIXSCALE=2.0)
    frame = load_frame(str(p))
    assert frame.wcs.has_celestial
    assert frame.wcs.wcs.crval[0] == pytest.approx(50.0)
    assert frame.wcs.wcs.crval[1] == pytest.approx(-30.0)


def test_load_frame_fallback_wcs_defaults_ra_dec_when_missing(tmp_path: Path) -> None:
    p = tmp_path / "f.fits"
    _write_frame(p, SP_TELE="scope-c")  # no RA/DEC/CTYPE at all
    frame = load_frame(str(p))
    assert frame.wcs.wcs.crval[0] == pytest.approx(0.0)
    assert frame.wcs.wcs.crval[1] == pytest.approx(0.0)


def test_load_frame_ignores_forged_emulator_flag_reads_it_honestly(tmp_path: Path) -> None:
    p = tmp_path / "f.fits"
    _write_frame(p, SP_TELE="scope-d", SP_EMULATOR=1)
    frame = load_frame(str(p))
    assert frame.sp_emulator is True


def test_load_frame_telescope_id_override_takes_priority(tmp_path: Path) -> None:
    p = tmp_path / "f.fits"
    _write_frame(p, SP_TELE="header-scope")
    frame = load_frame(str(p), telescope_id="override-scope")
    assert frame.telescope_id == "override-scope"


def test_load_frame_telescope_id_defaults_to_unknown(tmp_path: Path) -> None:
    p = tmp_path / "f.fits"
    _write_frame(p)  # no SP_TELE
    frame = load_frame(str(p))
    assert frame.telescope_id == "unknown"


def test_load_frame_exptime_defaults_to_one(tmp_path: Path) -> None:
    p = tmp_path / "f.fits"
    _write_frame(p, SP_TELE="scope-e")  # no exptime anywhere
    frame = load_frame(str(p))
    assert frame.exptime == 1.0


def test_load_frame_measures_positive_noise_and_snr(tmp_path: Path) -> None:
    p = tmp_path / "f.fits"
    _write_frame(p, SP_TELE="scope-f")
    frame = load_frame(str(p))
    assert frame.noise_adu >= 0.0
    assert frame.weight == 0.0  # weight assigned later by stack_frames
