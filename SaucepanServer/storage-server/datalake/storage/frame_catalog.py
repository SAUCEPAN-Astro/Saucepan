"""
frame_catalog — Persist denormalized L1 sky/time index on local SQLite.

Replaces the unused graded-frames SQLite precursor. Schema matches Alembic
``004_frame_catalog`` / PG ``0023_frame_catalog``.
"""

from __future__ import annotations

import logging
from datetime import datetime, timezone
from typing import Any

from sqlalchemy import select

from catalog import FrameCatalogRow
from db import session_scope

logger = logging.getLogger(__name__)


def upsert_frame_catalog(row: dict[str, Any]) -> dict[str, Any]:
    """Insert or update a frame_catalog row keyed by upload_id (or id)."""
    tele = row.get("telescope_id")
    object_key = row.get("object_key")
    if not tele or not object_key:
        raise ValueError("telescope_id and object_key are required")

    upload_id = row.get("upload_id")
    catalog_id = row.get("id")
    now = datetime.now(timezone.utc)

    with session_scope() as session:
        existing: FrameCatalogRow | None = None
        if upload_id:
            existing = session.execute(
                select(FrameCatalogRow).where(FrameCatalogRow.upload_id == upload_id)
            ).scalar_one_or_none()
        if existing is None and catalog_id:
            existing = session.get(FrameCatalogRow, catalog_id)

        if existing is None:
            existing = FrameCatalogRow(id=catalog_id)
            session.add(existing)

        existing.frame_id = row.get("frame_id")
        existing.upload_id = upload_id
        existing.telescope_id = str(tele)
        existing.task_id = str(row["task_id"]) if row.get("task_id") is not None else None
        existing.campaign_id = row.get("campaign_id")
        existing.object_key = str(object_key)
        existing.checksum_sha256 = row.get("checksum_sha256")
        existing.date_obs = row.get("date_obs")
        existing.mjd_obs = row.get("mjd_obs")
        existing.ra_deg = row.get("ra_deg")
        existing.dec_deg = row.get("dec_deg")
        existing.filter = row.get("filter")
        existing.exptime_sec = row.get("exptime_sec")
        existing.airmass = row.get("airmass")
        existing.fwhm_arcsec = row.get("fwhm_arcsec")
        existing.snr = row.get("snr")
        existing.tier = row.get("tier")
        existing.calstat = row.get("calstat")
        existing.phot_flag = row.get("phot_flag")
        existing.headline_grade = row.get("headline_grade")
        existing.stack_eligible = row.get("stack_eligible")
        existing.grade_json = row.get("grade_json")
        existing.zp = row.get("zp")
        if existing.created_at is None:
            existing.created_at = now

        session.flush()
        out = _row_to_dict(existing)

    logger.info(
        "frame_catalog upsert upload_id=%s ra=%s dec=%s",
        upload_id,
        out.get("ra_deg"),
        out.get("dec_deg"),
    )
    return out


def list_for_campaign(campaign_id: str, *, limit: int = 5000) -> list[dict[str, Any]]:
    """Return catalog rows for a campaign (feeds network rollup)."""
    with session_scope() as session:
        rows = (
            session.execute(
                select(FrameCatalogRow)
                .where(FrameCatalogRow.campaign_id == campaign_id)
                .order_by(FrameCatalogRow.date_obs.asc().nulls_last())
                .limit(limit)
            )
            .scalars()
            .all()
        )
        return [_row_to_dict(r) for r in rows]


def query_by_sky_time(
    *,
    ra_deg: float | None = None,
    dec_deg: float | None = None,
    radius_deg: float = 1.0,
    date_from: datetime | None = None,
    date_to: datetime | None = None,
    filter_name: str | None = None,
    limit: int = 500,
) -> list[dict[str, Any]]:
    """Simple box/cone-ish query for researcher handoff (#33 verify)."""
    with session_scope() as session:
        stmt = select(FrameCatalogRow)
        if ra_deg is not None and dec_deg is not None:
            stmt = stmt.where(
                FrameCatalogRow.ra_deg.is_not(None),
                FrameCatalogRow.dec_deg.is_not(None),
                FrameCatalogRow.ra_deg >= ra_deg - radius_deg,
                FrameCatalogRow.ra_deg <= ra_deg + radius_deg,
                FrameCatalogRow.dec_deg >= dec_deg - radius_deg,
                FrameCatalogRow.dec_deg <= dec_deg + radius_deg,
            )
        if date_from is not None:
            stmt = stmt.where(FrameCatalogRow.date_obs >= date_from)
        if date_to is not None:
            stmt = stmt.where(FrameCatalogRow.date_obs <= date_to)
        if filter_name:
            stmt = stmt.where(FrameCatalogRow.filter == filter_name)
        rows = session.execute(stmt.limit(limit)).scalars().all()
        return [_row_to_dict(r) for r in rows]


def _row_to_dict(row: FrameCatalogRow) -> dict[str, Any]:
    date_obs = row.date_obs
    return {
        "id": row.id,
        "frame_id": row.frame_id,
        "upload_id": row.upload_id,
        "telescope_id": row.telescope_id,
        "task_id": row.task_id,
        "campaign_id": row.campaign_id,
        "object_key": row.object_key,
        "checksum_sha256": row.checksum_sha256,
        "date_obs": date_obs.isoformat() if isinstance(date_obs, datetime) else date_obs,
        "mjd_obs": row.mjd_obs,
        "ra_deg": row.ra_deg,
        "dec_deg": row.dec_deg,
        "filter": row.filter,
        "exptime_sec": row.exptime_sec,
        "airmass": row.airmass,
        "fwhm_arcsec": row.fwhm_arcsec,
        "snr": row.snr,
        "tier": row.tier,
        "calstat": row.calstat,
        "phot_flag": row.phot_flag,
        "headline_grade": row.headline_grade,
        "stack_eligible": row.stack_eligible,
        "grade_json": row.grade_json,
        "zp": row.zp,
        "created_at": row.created_at.isoformat() if row.created_at else None,
    }
