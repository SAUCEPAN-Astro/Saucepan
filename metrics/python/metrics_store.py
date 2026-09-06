"""Persist metric observations (datalake SQLite)."""

from __future__ import annotations

import json
import os
import uuid
from contextlib import contextmanager
from datetime import datetime, timezone
from typing import Any

from sqlalchemy import DateTime, Index, String, Text
from sqlalchemy.orm import Mapped, declarative_base, mapped_column, sessionmaker

from metrics.privacy import sanitize_observation

try:
    from db import Base as _DatalakeBase
    from db import session_scope as _datalake_session_scope
except ModuleNotFoundError as exc:
    if exc.name != "db":
        raise
    _DatalakeBase = declarative_base()
    _datalake_session_scope = None
    _STANDALONE = True
else:
    _STANDALONE = False

Base = _DatalakeBase
_standalone_engine = None
_standalone_session_factory = None


def _get_standalone_session_factory():
    global _standalone_engine, _standalone_session_factory
    if _standalone_session_factory is None:
        url = os.environ.get("METRICS_DATABASE_URL", os.environ.get("DATABASE_URL", "sqlite:///metrics.db"))
        connect_args = {"check_same_thread": False} if url.startswith("sqlite") else {}
        from sqlalchemy import create_engine

        _standalone_engine = create_engine(url, future=True, connect_args=connect_args)
        _standalone_session_factory = sessionmaker(
            bind=_standalone_engine, autoflush=False, autocommit=False
        )
        Base.metadata.create_all(bind=_standalone_engine)
    return _standalone_session_factory


@contextmanager
def _session_scope():
    if not _STANDALONE:
        with _datalake_session_scope() as session:
            yield session
        return

    session = _get_standalone_session_factory()()
    try:
        yield session
        session.commit()
    except Exception:
        session.rollback()
        raise
    finally:
        session.close()


def _utcnow() -> datetime:
    return datetime.now(timezone.utc)


class MetricObservation(Base):
    __tablename__ = "metric_observations"

    id: Mapped[str] = mapped_column(String(36), primary_key=True, default=lambda: str(uuid.uuid4()))
    upload_id: Mapped[str | None] = mapped_column(String(36), nullable=True)
    frame_id: Mapped[str | None] = mapped_column(String(36), nullable=True)
    telescope_id: Mapped[str | None] = mapped_column(String(128), nullable=True)
    node_id: Mapped[str | None] = mapped_column(String(128), nullable=True)
    producer: Mapped[str] = mapped_column(String(64), nullable=False)
    observed_at: Mapped[datetime] = mapped_column(
        DateTime(timezone=True), nullable=False, default=_utcnow
    )
    metrics_json: Mapped[str] = mapped_column(Text, nullable=False)
    context_json: Mapped[str] = mapped_column(Text, nullable=False)
    wait_pile_json: Mapped[str] = mapped_column(Text, nullable=False, default="[]")

    __table_args__ = (
        Index("ix_metric_obs_upload", "upload_id"),
        Index("ix_metric_obs_tele", "telescope_id"),
    )


def save_metric_observation(observation: dict[str, Any]) -> None:
    safe_observation = sanitize_observation(observation)
    ctx = safe_observation.get("context") or {}
    with _session_scope() as session:
        row = MetricObservation(
            id=safe_observation.get("observation_id") or str(uuid.uuid4()),
            upload_id=ctx.get("upload_id"),
            frame_id=ctx.get("frame_id"),
            telescope_id=ctx.get("telescope_id"),
            node_id=ctx.get("node_id"),
            producer=safe_observation.get("producer", ""),
            metrics_json=json.dumps(safe_observation.get("metrics") or {}),
            context_json=json.dumps(ctx),
            wait_pile_json=json.dumps(safe_observation.get("wait_pile") or []),
        )
        session.add(row)


def list_wait_pile_for_upload(upload_id: str) -> list[str]:
    with _session_scope() as session:
        rows = (
            session.query(MetricObservation)
            .filter(MetricObservation.upload_id == upload_id)
            .order_by(MetricObservation.observed_at.desc())
            .limit(1)
            .all()
        )
        if not rows:
            return []
        return json.loads(rows[0].wait_pile_json or "[]")


def list_l1_frames_for_night(
    telescope_id: str,
    night_id: str,
    *,
    limit: int = 5000,
) -> list[dict[str, Any]]:
    """
    Load L1 frame rows for session rollup (#21).

    Prefers ``frame_catalog`` (sky/time index). Falls back to merging
    ``metric_observations`` frame_* payloads when catalog is empty.
    """
    night_day = night_id.split("_")[-1] if "_" in night_id else night_id
    frames: list[dict[str, Any]] = []

    try:
        from catalog import FrameCatalogRow
        from sqlalchemy import select

        with _session_scope() as session:
            # Match telescope + UTC date prefix on date_obs
            stmt = (
                select(FrameCatalogRow)
                .where(FrameCatalogRow.telescope_id == telescope_id)
                .where(FrameCatalogRow.date_obs.is_not(None))
                .limit(limit)
            )
            rows = session.execute(stmt).scalars().all()
            for row in rows:
                if row.date_obs is None:
                    continue
                day = row.date_obs.date().isoformat()
                if day != night_day and night_id not in (
                    f"{telescope_id}_{day}",
                    day,
                ):
                    continue
                frames.append(
                    {
                        "telescope_id": row.telescope_id,
                        "fwhm_arcsec": row.fwhm_arcsec,
                        "airmass": row.airmass,
                        "zp": row.zp,
                        "exptime_sec": row.exptime_sec,
                        "stack_eligible": row.stack_eligible,
                        "rejected": row.stack_eligible is False,
                        "date_obs": row.date_obs.isoformat() if row.date_obs else None,
                        "ra_deg": row.ra_deg,
                        "dec_deg": row.dec_deg,
                        "filter": row.filter,
                        "snr": row.snr,
                    }
                )
    except Exception:
        frames = []

    if frames:
        return frames

    # Fallback: scrape stored metric observations
    with _session_scope() as session:
        rows = (
            session.query(MetricObservation)
            .filter(MetricObservation.telescope_id == telescope_id)
            .order_by(MetricObservation.observed_at.desc())
            .limit(limit)
            .all()
        )
        for row in rows:
            metrics = json.loads(row.metrics_json or "{}")
            ctx = json.loads(row.context_json or "{}")
            obs_night = ctx.get("night_id") or ""
            if night_id not in (obs_night, night_day) and not str(obs_night).endswith(night_day):
                continue
            frames.append(
                {
                    "telescope_id": telescope_id,
                    "fwhm_arcsec": metrics.get("frame.fwhm_arcsec"),
                    "airmass": metrics.get("frame.airmass"),
                    "zp": metrics.get("frame.zp"),
                    "exptime_sec": metrics.get("frame.exptime_sec"),
                    "stack_eligible": metrics.get("grade.stack_eligible"),
                    "plate_solve_ok": metrics.get("frame.plate_solve_ok"),
                    "comp_rms_mag": metrics.get("lp.comp_rms_mag"),
                }
            )
    return frames
