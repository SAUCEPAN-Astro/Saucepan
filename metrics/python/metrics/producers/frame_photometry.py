"""Producer: photometry outputs (SP_ZP, depth, flags)."""

from __future__ import annotations

import typing

from metrics.observation import EntityContext

_RESULT_MAP: dict[str, str] = {
    "frame.skymag": "skymag",
    "frame.zp": "zp",
    "frame.zp_rms": "zp_rms",
    "frame.limmag_5sigma": "limmag_5sigma",
    "frame.phot_flag": "phot_flag",
}

_HEADER_MAP: dict[str, str] = {
    "frame.skymag": "sp_skymag",
    "frame.zp": "sp_zp",
    "frame.zp_rms": "sp_zprms",
    "frame.limmag_5sigma": "sp_limmg5",
    "frame.phot_flag": "sp_photflag",
}


def _from_result(result: dict[str, typing.Any]) -> dict[str, typing.Any]:
    out: dict[str, typing.Any] = {}
    for metric_id, key in _RESULT_MAP.items():
        val = result.get(key)
        if val is not None:
            out[metric_id] = val
    return out


def _read_headers(path: str) -> dict[str, typing.Any]:
    try:
        from astropy.io import fits
    except ImportError:
        return {}

    try:
        with fits.open(path, memmap=True) as hdul:
            hdr = hdul[0].header
            return {k.lower(): hdr.get(k) for k in hdr.keys()}
    except OSError:
        return {}


def produce(ctx: EntityContext) -> dict[str, typing.Any]:
    result = ctx.get("_photometry_result")
    if isinstance(result, dict):
        metrics = _from_result(result)
        if metrics:
            return metrics

    path = ctx.get("staged_path")
    if not path:
        return {}

    hdr = _read_headers(path)
    out: dict[str, typing.Any] = {}
    for metric_id, hdr_key in _HEADER_MAP.items():
        val = hdr.get(hdr_key)
        if val is not None:
            out[metric_id] = val
    return out
