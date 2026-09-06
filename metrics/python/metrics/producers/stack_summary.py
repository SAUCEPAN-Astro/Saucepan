"""Producer: stack product metrics from API summary + output FITS headers."""

from __future__ import annotations

import typing

from metrics.observation import EntityContext


def _header_float(hdr: dict[str, typing.Any], *keys: str) -> typing.Any:
    for key in keys:
        val = hdr.get(key)
        if val is not None:
            return val
    return None


def _read_stack_headers(path: str | None) -> dict[str, typing.Any]:
    if not path:
        return {}
    try:
        from astropy.io import fits
    except ImportError:
        return {}
    try:
        with fits.open(path, memmap=True) as hdul:
            return {k.lower(): hdul[0].header.get(k) for k in hdul[0].header.keys()}
    except OSError:
        return {}


def produce(ctx: EntityContext) -> dict[str, typing.Any]:
    summary = dict(ctx.get("_stack_summary") or {})
    hdr = _read_stack_headers(ctx.get("stack_output_path"))

    def _pick(summary_key: str, *hdr_keys: str):
        val = summary.get(summary_key)
        if val is not None:
            return val
        return _header_float(hdr, *hdr_keys)

    out: dict[str, typing.Any] = {}
    n_used = _pick("n_frames_used", "sp_nstack")
    if n_used is not None:
        out["stack.n_frames"] = n_used
        out["stack.nstack"] = n_used
    n_reject = _pick("n_frames_rejected", "sp_nreject")
    if n_reject is not None:
        out["stack.n_reject"] = n_reject
    snr = _pick("stack_snr", "sp_snr")
    if snr is not None:
        out["stack.snr"] = snr
    snr_gain = _pick("snr_gain", "sp_snrgn")
    if snr_gain is not None:
        out["stack.snr_gain"] = snr_gain
    efficiency = _pick("efficiency", "sp_eff")
    if efficiency is not None:
        out["stack.efficiency"] = efficiency
    thmax = _pick("theoretical_max", "sp_thmax")
    if thmax is not None:
        out["stack.thmax"] = thmax
    coverage = _header_float(hdr, "sp_cov")
    if coverage is not None:
        out["stack.coverage"] = coverage

    stack_method = _header_float(hdr, "sp_stackmth")
    if stack_method is not None:
        out["stack.stack_method"] = stack_method
    weight_method = _header_float(hdr, "sp_wgtmth")
    if weight_method is not None:
        out["stack.weight_method"] = weight_method
    sigclip = _header_float(hdr, "sp_sigclip")
    if sigclip is not None:
        out["stack.sigclip"] = sigclip
    reproj = _header_float(hdr, "sp_reproj")
    if reproj is not None:
        out["stack.reproj"] = reproj
    tpsffw = _header_float(hdr, "sp_tpsffw")
    if tpsffw is not None:
        out["stack.tpsffw"] = tpsffw

    provenance = summary.get("provenance") or []
    used = [p for p in provenance if not p.get("rejected")]
    if used:
        primary = used[0]
        if primary.get("weight_pct") is not None:
            out["stack.provenance_weight_pct"] = primary["weight_pct"]
        if primary.get("weight") is not None:
            out["stack.provenance_weight"] = primary["weight"]
        if primary.get("telescope_id"):
            out["stack.provenance_tele_id"] = primary["telescope_id"]
        rejected = [p for p in provenance if p.get("rejected")]
        if rejected:
            out["stack.provenance_rejected"] = len(rejected)

    return out
