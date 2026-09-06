"""Photometry table row contract for time-domain products (#422).

Default science product for ``product.mode=per_frame`` / ``time_bin`` is
calibrated frames plus one table row per bin — not a deep stack.
"""

from __future__ import annotations

import typing

# Documented fields (AC #422). Check-star columns are optional when present.
TABLE_FIELDS = (
    "time",  # UTC / SP_DATEOBS (ISO-8601 or MJD)
    "mag",  # differential or standard magnitude
    "mag_err",
    "comp_stars",  # list of {id, ra?, dec?, ref_mag?, band?}
    "airmass",
    "filter",  # SP_FILTER / band
    "check_star",  # optional {id, mag?, check_minus_comp?}
    "check_minus_comp",  # optional scalar convenience
)

REQUIRED_FIELDS = ("time", "mag", "mag_err", "comp_stars", "airmass", "filter")


def build_table_row(
    *,
    time: str | float | None,
    mag: float | None,
    mag_err: float | None = None,
    comp_stars: list[dict[str, typing.Any]] | None = None,
    airmass: float | None = None,
    filter_name: str | None = None,
    check_star: dict[str, typing.Any] | None = None,
    check_minus_comp: float | None = None,
    extra: dict[str, typing.Any] | None = None,
) -> dict[str, typing.Any]:
    """Build one photometry-table row with the documented #422 fields."""
    row: dict[str, typing.Any] = {
        "time": time,
        "mag": mag,
        "mag_err": mag_err,
        "comp_stars": list(comp_stars or []),
        "airmass": airmass,
        "filter": filter_name,
    }
    if check_star is not None:
        row["check_star"] = check_star
    if check_minus_comp is not None:
        row["check_minus_comp"] = check_minus_comp
    if extra:
        row.update(extra)
    return row


def row_from_lp(
    lp_result: dict[str, typing.Any],
    *,
    hdr: dict[str, typing.Any] | None = None,
    ctx: dict[str, typing.Any] | None = None,
) -> dict[str, typing.Any]:
    """Map an LP result (+ FITS/ctx metadata) onto the photometry table row."""
    hdr = hdr or {}
    ctx = ctx or {}
    time = hdr.get("SP_DATEOBS") or hdr.get("DATE-OBS") or ctx.get("date_obs") or ctx.get("time")
    filt = hdr.get("SP_FILTER") or hdr.get("FILTER") or ctx.get("filter")
    airmass = hdr.get("SP_AIRMASS") or hdr.get("AIRMASS") or ctx.get("airmass")
    try:
        airmass_f = float(airmass) if airmass is not None else None
    except (TypeError, ValueError):
        airmass_f = None

    mag = lp_result.get("lp.std_mag")
    if mag is None:
        mag = lp_result.get("lp.delta_mag")
    if mag is None:
        mag = lp_result.get("lp.inst_mag")
    mag_err = lp_result.get("lp.mag_err")

    comps_raw = ctx.get("campaign_comp_stars") or []
    comps: list[dict[str, typing.Any]] = []
    if isinstance(comps_raw, list):
        for s in comps_raw:
            if not isinstance(s, dict):
                continue
            role = str(s.get("role") or "comp").lower()
            if role == "check":
                continue
            comps.append(
                {
                    "id": s.get("id") or s.get("name") or "comp",
                    "ra": s.get("ra"),
                    "dec": s.get("dec"),
                    "ref_mag": s.get("ref_mag") or s.get("mag"),
                    "band": s.get("band") or s.get("filter"),
                }
            )
    if not comps and lp_result.get("lp.comp_id") is not None:
        comps = [
            {
                "id": lp_result.get("lp.comp_id"),
                "ref_mag": lp_result.get("lp.comp_ref_mag"),
            }
        ]

    check = None
    if lp_result.get("lp.check_id") is not None:
        check = {
            "id": lp_result["lp.check_id"],
            "inst_mag": lp_result.get("lp.check_inst_mag"),
            "check_minus_comp": lp_result.get("lp.check_minus_comp"),
        }

    return build_table_row(
        time=time,
        mag=float(mag) if mag is not None else None,
        mag_err=float(mag_err) if mag_err is not None else None,
        comp_stars=comps,
        airmass=airmass_f,
        filter_name=str(filt) if filt is not None else None,
        check_star=check,
        check_minus_comp=lp_result.get("lp.check_minus_comp"),
    )


def validate_row_shape(row: dict[str, typing.Any]) -> list[str]:
    """Return missing required field names (empty = ok)."""
    return [k for k in REQUIRED_FIELDS if k not in row]
