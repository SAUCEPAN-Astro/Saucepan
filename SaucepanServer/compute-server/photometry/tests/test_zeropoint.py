"""Catalog-matched zeropoint (#203).

Synthetic fields with a known injected ZP must be recovered within a frozen
tolerance; degenerate fields (too few matches, high scatter, no reference)
must fail closed with no number. No network — reference stars are handed in
inline or via a local file.
"""

from __future__ import annotations

import json
import math

import numpy as np
import pytest
from astropy.wcs import WCS
from photometry import zeropoint

ZP_TRUE = 24.83
TOL_MAG = 0.02


def _matches(n, zp=ZP_TRUE, noise=0.005, seed=0, ref_err=0.01):
    rng = np.random.default_rng(seed)
    inst = rng.uniform(-12.0, -6.0, size=n)
    ref = inst + zp + rng.normal(0.0, noise, size=n)
    return [
        {"inst_mag": float(i), "ref_mag": float(r), "ref_err": ref_err}
        for i, r in zip(inst, ref)
    ]


# ── fit_zeropoint: pure robust fit ──────────────────────────────────────


def test_recovers_injected_zp_within_tolerance():
    out = zeropoint.fit_zeropoint(_matches(40), catalog="synthetic")
    assert out["ok"] is True
    assert out["zp"] is not None
    assert abs(out["zp"] - ZP_TRUE) < TOL_MAG
    assert out["n_cal_stars"] == 40
    assert out["catalog"] == "synthetic"


def test_robust_to_a_gross_outlier():
    m = _matches(30, noise=0.004, seed=3)
    m[7]["ref_mag"] += 5.0  # one catastrophic mismatch
    out = zeropoint.fit_zeropoint(m)
    assert out["ok"] is True
    assert abs(out["zp"] - ZP_TRUE) < TOL_MAG


def test_too_few_matches_fails_closed():
    out = zeropoint.fit_zeropoint(_matches(3))
    assert out["ok"] is False
    assert out["zp"] is None
    assert out["reason"] == "insufficient_matches"
    assert out["n_cal_stars"] == 3


def test_high_scatter_fails_closed():
    out = zeropoint.fit_zeropoint(_matches(40, noise=1.5, seed=9), max_zprms=0.15)
    assert out["ok"] is False
    assert out["zp"] is None
    assert out["reason"] == "zp_rms_exceeds_max"


def test_env_overrides_min_cal_stars(monkeypatch):
    monkeypatch.setenv("PHOT_MIN_CAL_STARS", "20")
    out = zeropoint.fit_zeropoint(_matches(10))
    assert out["ok"] is False
    assert out["reason"] == "insufficient_matches"


def test_color_term_applied_when_supplied():
    m = _matches(30, noise=0.0, seed=1)
    for k, entry in enumerate(m):
        entry["color"] = 0.5
        entry["ref_mag"] += 0.1 * 0.5  # planted colour dependence, k_c = 0.1
    out = zeropoint.fit_zeropoint(m, color_term=0.1)
    assert out["ok"] is True
    assert abs(out["zp"] - ZP_TRUE) < TOL_MAG
    assert out["color_term"] == 0.1


# ── zeropoint_for_frame: match + fit against a known WCS ────────────────


def _tan_wcs(nx=256, ny=256, ra0=150.0, dec0=2.0, scale_deg=1.0 / 3600.0):
    w = WCS(naxis=2)
    w.wcs.crpix = [nx / 2, ny / 2]
    w.wcs.cdelt = [-scale_deg, scale_deg]
    w.wcs.crval = [ra0, dec0]
    w.wcs.ctype = ["RA---TAN", "DEC--TAN"]
    return w


def _synthetic_frame(n=25, zp=ZP_TRUE, exptime=30.0, seed=0):
    rng = np.random.default_rng(seed)
    w = _tan_wcs()
    xs = rng.uniform(20, 235, size=n)
    ys = rng.uniform(20, 235, size=n)
    flux = rng.uniform(2_000.0, 200_000.0, size=n)
    inst_mag = -2.5 * np.log10(flux / exptime)
    ra, dec = w.all_pix2world(xs, ys, 0)
    refs = [
        {"ra": float(r), "dec": float(d), "mag": float(m + zp), "mag_err": 0.01}
        for r, d, m in zip(ra, dec, inst_mag)
    ]
    sources = {"x": xs, "y": ys, "flux": flux}
    hdr = dict(w.to_header())
    hdr["SP_EXPTIME"] = exptime
    hdr["MJD-OBS"] = 60000.5
    plate = {"ok": True, "ra": 150.0, "dec": 2.0}
    return sources, plate, hdr, refs


def test_frame_recovers_zp_from_inline_reference():
    sources, plate, hdr, refs = _synthetic_frame()
    out = zeropoint.zeropoint_for_frame(sources, plate, hdr, {"phot_reference": refs})
    assert out["ok"] is True
    assert abs(out["zp"] - ZP_TRUE) < TOL_MAG
    assert out["epoch"] == 60000.5
    assert out["catalog"].endswith(":inline")


def test_frame_recovers_zp_from_local_file(tmp_path):
    sources, plate, hdr, refs = _synthetic_frame(seed=2)
    path = tmp_path / "refcat.json"
    path.write_text(json.dumps(refs), encoding="utf-8")
    out = zeropoint.zeropoint_for_frame(
        sources, plate, hdr, {"phot_reference_file": str(path)}
    )
    assert out["ok"] is True
    assert abs(out["zp"] - ZP_TRUE) < TOL_MAG
    assert out["catalog"].endswith(":file")


def test_frame_no_reference_fails_closed():
    sources, plate, hdr, _ = _synthetic_frame()
    out = zeropoint.zeropoint_for_frame(sources, plate, hdr, {})
    assert out["ok"] is False
    assert out["zp"] is None
    assert out["reason"] == "no_reference_catalog"


def test_frame_plate_solve_failed_fails_closed():
    sources, _, hdr, refs = _synthetic_frame()
    out = zeropoint.zeropoint_for_frame(
        sources, {"ok": False}, hdr, {"phot_reference": refs}
    )
    assert out["ok"] is False
    assert out["reason"] == "plate_solve_failed"


def test_frame_no_wcs_fails_closed():
    sources, plate, _, refs = _synthetic_frame()
    out = zeropoint.zeropoint_for_frame(
        sources, plate, {"SP_EXPTIME": 30.0}, {"phot_reference": refs}
    )
    assert out["ok"] is False
    assert out["reason"] == "no_wcs_for_match"


def test_frame_too_few_in_frame_matches_fails_closed():
    sources, plate, hdr, refs = _synthetic_frame(n=25)
    # Keep only 3 reference stars -> below PHOT_MIN_CAL_STARS.
    out = zeropoint.zeropoint_for_frame(
        sources, plate, hdr, {"phot_reference": refs[:3]}
    )
    assert out["ok"] is False
    assert out["zp"] is None
    assert "insufficient" in out["reason"]


def test_weighted_median_matches_numpy_on_uniform_weights():
    vals = np.array([1.0, 2.0, 3.0, 4.0, 100.0])
    w = np.ones_like(vals)
    assert zeropoint._weighted_median(vals, w) == pytest.approx(3.0)
