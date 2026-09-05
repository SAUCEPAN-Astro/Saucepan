"""
Build frame_catalog row dicts from SP_* headers + grade payload.

SSOT mapper for the denormalized L1 index (METADATA Phase 2). Reuses
``read_sp_headers`` — do not reimplement header parsing here.
"""

from __future__ import annotations

import math
import typing
import uuid
from datetime import datetime, timezone

from grading.fits_reader import read_sp_headers


def _finite_float(value: typing.Any) -> float | None:
    """Return a finite float for JSON-safe catalog fields."""
    try:
        number = float(value)
    except (TypeError, ValueError):
        return None
    return number if math.isfinite(number) else None


def _headline_int(value: typing.Any) -> int | None:
    """Return a bounded integer headline grade for catalog JSON."""
    number = _finite_float(value)
    if number is None:
        return None
    return max(0, min(100, int(round(number))))


def _json_safe(value: typing.Any) -> typing.Any:
    """Replace non-finite numbers in nested grade metadata with ``None``."""
    if isinstance(value, dict):
        return {str(key): _json_safe(item) for key, item in value.items()}
    if isinstance(value, (list, tuple)):
        return [_json_safe(item) for item in value]
    if isinstance(value, float) and not math.isfinite(value):
        return None
    return value


def _mjd_from_dateobs(date_obs: str | datetime | None) -> float | None:
    if date_obs is None:
        return None
    try:
        from astropy.time import Time

        if isinstance(date_obs, datetime):
            return _finite_float(Time(date_obs).mjd)
        text = str(date_obs).strip()
        parsed = _parse_date_obs(text)
        if parsed is not None:
            return _finite_float(Time(parsed).mjd)
        if text.endswith("Z"):
            text = text[:-1]
        # astropy iso parser rejects explicit +00:00 offsets
        if text.endswith("+00:00"):
            text = text[:-6]
        elif "+" in text[10:] or text.count("-") > 2:
            # drop trailing offset like -05:00 / +01:00
            for sep in ("+", "-"):
                idx = text.rfind(sep)
                if idx > 10:
                    text = text[:idx]
                    break
        return _finite_float(Time(text, scale="utc").mjd)
    except Exception:
        return None


def _parse_date_obs(raw: str | None) -> datetime | None:
    if not raw:
        return None
    text = str(raw).strip()
    if not text:
        return None
    if text.endswith("Z"):
        text = text[:-1] + "+00:00"
    try:
        dt = datetime.fromisoformat(text)
    except ValueError:
        return None
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=timezone.utc)
    return dt


def _dim_float(dimensions: typing.Mapping[str, typing.Any], *path: str) -> float | None:
    cur: typing.Any = dimensions
    for key in path:
        if not isinstance(cur, dict):
            return None
        cur = cur.get(key)
    if cur is None:
        return None
    return _finite_float(cur)


def row_from_headers_and_grade(
    headers: typing.Mapping[str, typing.Any],
    *,
    grade: typing.Mapping[str, typing.Any] | None = None,
    upload_id: str | None = None,
    frame_id: str | None = None,
    telescope_id: str | None = None,
    task_id: str | int | None = None,
    campaign_id: str | None = None,
    object_key: str | None = None,
    checksum_sha256: str | None = None,
    zp: float | None = None,
    phot_flag: str | None = None,
    airmass: float | None = None,
    tier: int | None = None,
    catalog_id: str | None = None,
) -> dict[str, typing.Any]:
    """Map headers + grade into a frame_catalog row dict (no DB I/O)."""
    grade = grade or {}
    dimensions = grade.get("dimensions") if isinstance(grade.get("dimensions"), dict) else {}
    quality = grade.get("quality_metrics") if isinstance(grade.get("quality_metrics"), dict) else {}

    date_obs_raw = headers.get("sp_dateobs")
    date_obs = _parse_date_obs(date_obs_raw if isinstance(date_obs_raw, str) else None)

    fwhm = _finite_float(headers.get("sp_fwhm"))
    if fwhm is None:
        fwhm = _dim_float(dimensions, "image_quality", "fwhm_arcsec")

    snr = _finite_float(headers.get("sp_snr"))
    if snr is None:
        snr = _finite_float(quality.get("snr"))

    exptime = _finite_float(headers.get("sp_exptime"))
    if exptime is None:
        exptime = _finite_float(grade.get("sp_exptime"))

    filter_name = headers.get("sp_filter")
    if not filter_name:
        tf = dimensions.get("task_fidelity") if isinstance(dimensions, dict) else None
        if isinstance(tf, dict):
            filter_name = tf.get("filter_actual") or tf.get("filter_requested")

    calstat = headers.get("sp_calstat")
    if not calstat:
        iq = dimensions.get("image_quality") if isinstance(dimensions, dict) else None
        if isinstance(iq, dict):
            calstat = iq.get("calstat")

    tele = telescope_id or grade.get("telescope_id")
    if not tele:
        raise ValueError("telescope_id required for frame_catalog row")
    key = object_key or grade.get("object_key") or f"upload/{upload_id or 'unknown'}"

    row: dict[str, typing.Any] = {
        "id": catalog_id or str(uuid.uuid4()),
        "frame_id": frame_id,
        "upload_id": upload_id or grade.get("upload_id"),
        "telescope_id": str(tele),
        "task_id": str(task_id)
        if task_id is not None
        else (str(grade["task_id"]) if grade.get("task_id") is not None else None),
        "campaign_id": campaign_id,
        "object_key": str(key),
        "checksum_sha256": checksum_sha256,
        "date_obs": date_obs,
        "mjd_obs": _mjd_from_dateobs(
            date_obs or (date_obs_raw if isinstance(date_obs_raw, str) else None)
        ),
        "ra_deg": _finite_float(headers.get("sp_ra")),
        "dec_deg": _finite_float(headers.get("sp_dec")),
        "filter": filter_name,
        "exptime_sec": exptime,
        "airmass": _finite_float(airmass),
        "fwhm_arcsec": fwhm,
        "snr": snr,
        "tier": tier if tier is not None else _tier_from_grade(grade),
        "calstat": calstat,
        "phot_flag": phot_flag,
        "headline_grade": _headline_int(grade.get("headline")),
        "stack_eligible": grade.get("stack_eligible") if isinstance(grade.get("stack_eligible"), bool) else None,
        "grade_json": _json_safe(grade) if grade else None,
        "zp": _finite_float(zp),
    }
    return row


def _tier_from_grade(grade: typing.Mapping[str, typing.Any]) -> int | None:
    data_tier = grade.get("data_tier")
    if data_tier is None:
        return None
    mapping = {"science": 1, "engineering": 2, "test": 3, "emulator": 4}
    if isinstance(data_tier, int):
        return data_tier
    return mapping.get(str(data_tier).lower())


def extract_from_fits(
    fits_path: str,
    *,
    grade: typing.Mapping[str, typing.Any] | None = None,
    **meta: typing.Any,
) -> dict[str, typing.Any]:
    """Read SP_* from FITS and build a catalog row."""
    headers = read_sp_headers(fits_path)
    return row_from_headers_and_grade(headers, grade=grade, **meta)


def catalog_fields_for_ingest(row: typing.Mapping[str, typing.Any]) -> dict[str, typing.Any]:
    """Subset embedded on grades-ingest body as ``frame_catalog``."""
    date_obs = row.get("date_obs")
    if isinstance(date_obs, datetime):
        date_obs = date_obs.isoformat()
    return {
        "id": row.get("id"),
        "frame_id": row.get("frame_id"),
        "upload_id": row.get("upload_id"),
        "telescope_id": row.get("telescope_id"),
        "task_id": row.get("task_id"),
        "campaign_id": row.get("campaign_id"),
        "object_key": row.get("object_key"),
        "checksum_sha256": row.get("checksum_sha256"),
        "date_obs": date_obs,
        "mjd_obs": row.get("mjd_obs"),
        "ra_deg": row.get("ra_deg"),
        "dec_deg": row.get("dec_deg"),
        "filter": row.get("filter"),
        "exptime_sec": row.get("exptime_sec"),
        "airmass": row.get("airmass"),
        "fwhm_arcsec": row.get("fwhm_arcsec"),
        "snr": row.get("snr"),
        "tier": row.get("tier"),
        "calstat": row.get("calstat"),
        "phot_flag": row.get("phot_flag"),
        "headline_grade": row.get("headline_grade"),
        "stack_eligible": row.get("stack_eligible"),
        "zp": row.get("zp"),
    }
