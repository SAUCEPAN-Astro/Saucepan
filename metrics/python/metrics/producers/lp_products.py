"""Producer: LP differential photometry products."""

from __future__ import annotations

import typing

from metrics.observation import EntityContext

_LP_KEYS = (
    "lp.source_snr",
    "lp.inst_mag",
    "lp.mag_err",
    "lp.std_mag",
    "lp.delta_mag",
    "lp.comp_id",
    "lp.comp_inst_mag",
    "lp.comp_ref_mag",
    "lp.comp_ref_err",
    "lp.check_id",
    "lp.check_inst_mag",
    "lp.check_minus_comp",
    "lp.ensemble_weight",
    "lp.aperture_correction",
    "lp.aperture_radius",
    # #419 standard-system transform (registry stubs → live producer keys)
    "lp.color_term",
    "lp.transform_coeff",
    "lp.transform_applied",
)


def produce(ctx: EntityContext) -> dict[str, typing.Any]:
    result = ctx.get("_lp_result")
    if not isinstance(result, dict):
        return {}

    out: dict[str, typing.Any] = {}
    for key in _LP_KEYS:
        if key in result:
            out[key] = result[key]
    return out
