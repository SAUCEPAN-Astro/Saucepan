"""Catalog ORM models — uploads, frames, jobs, L1 frame catalog."""

from __future__ import annotations

import uuid
from datetime import datetime, timezone

from sqlalchemy import (
    BigInteger,
    Boolean,
    DateTime,
    Float,
    ForeignKey,
    Index,
    Integer,
    String,
    Text,
)
from sqlalchemy.orm import Mapped, mapped_column, relationship
from sqlalchemy.types import JSON

from db import Base


def _utcnow() -> datetime:
    return datetime.now(timezone.utc)


def _new_id() -> str:
    return str(uuid.uuid4())


class Upload(Base):
    __tablename__ = "uploads"

    id: Mapped[str] = mapped_column(String(36), primary_key=True, default=_new_id)
    status: Mapped[str] = mapped_column(String(32), nullable=False, default="pending")
    bucket: Mapped[str] = mapped_column(String(255), nullable=False)
    object_key: Mapped[str] = mapped_column(String(1024), nullable=False)
    filename: Mapped[str] = mapped_column(String(512), nullable=False)
    campaign_id: Mapped[str] = mapped_column(String(128), nullable=False)
    task_id: Mapped[str | None] = mapped_column(String(64), nullable=True)
    telescope_id: Mapped[str | None] = mapped_column(String(128), nullable=True)
    content_type: Mapped[str] = mapped_column(
        String(128), nullable=False, default="application/fits"
    )
    size_bytes: Mapped[int | None] = mapped_column(BigInteger, nullable=True)
    etag: Mapped[str | None] = mapped_column(String(128), nullable=True)
    metadata_json: Mapped[dict | None] = mapped_column(JSON, nullable=True)
    created_at: Mapped[datetime] = mapped_column(
        DateTime(timezone=True), nullable=False, default=_utcnow
    )
    completed_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)

    frames: Mapped[list["Frame"]] = relationship(back_populates="upload")

    __table_args__ = (
        Index("ix_uploads_status", "status"),
        Index("ix_uploads_object_key", "object_key"),
    )


class Frame(Base):
    __tablename__ = "frames"

    id: Mapped[str] = mapped_column(String(36), primary_key=True, default=_new_id)
    upload_id: Mapped[str] = mapped_column(String(36), ForeignKey("uploads.id"), nullable=False)
    campaign_id: Mapped[str] = mapped_column(String(128), nullable=False)
    object_key: Mapped[str] = mapped_column(String(1024), nullable=False)
    staged_path: Mapped[str | None] = mapped_column(String(2048), nullable=True)
    checksum_sha256: Mapped[str | None] = mapped_column(String(64), nullable=True)
    size_bytes: Mapped[int | None] = mapped_column(BigInteger, nullable=True)
    grade_status: Mapped[str | None] = mapped_column(String(64), nullable=True)
    headline_grade: Mapped[int | None] = mapped_column(nullable=True)
    grade_json: Mapped[dict | None] = mapped_column(JSON, nullable=True)
    ingest_status: Mapped[str | None] = mapped_column(String(32), nullable=True)
    created_at: Mapped[datetime] = mapped_column(
        DateTime(timezone=True), nullable=False, default=_utcnow
    )

    upload: Mapped["Upload"] = relationship(back_populates="frames")
    jobs: Mapped[list["Job"]] = relationship(back_populates="frame")

    __table_args__ = (Index("ux_frames_upload_id", "upload_id", unique=True),)


class Job(Base):
    __tablename__ = "jobs"

    id: Mapped[str] = mapped_column(String(36), primary_key=True, default=_new_id)
    job_type: Mapped[str] = mapped_column(String(64), nullable=False)
    frame_id: Mapped[str | None] = mapped_column(String(36), ForeignKey("frames.id"), nullable=True)
    payload_json: Mapped[dict | None] = mapped_column(JSON, nullable=True)
    status: Mapped[str] = mapped_column(String(32), nullable=False, default="pending")
    error_message: Mapped[str | None] = mapped_column(Text, nullable=True)
    created_at: Mapped[datetime] = mapped_column(
        DateTime(timezone=True), nullable=False, default=_utcnow
    )
    started_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)
    completed_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)

    frame: Mapped["Frame | None"] = relationship(back_populates="jobs")

    __table_args__ = (
        Index("ix_jobs_status", "status"),
        Index("ix_jobs_job_type", "job_type"),
    )


class FrameCatalogRow(Base):
    """Denormalized L1 index — sky/time queryable (METADATA Phase 2)."""

    __tablename__ = "frame_catalog"

    id: Mapped[str] = mapped_column(String(36), primary_key=True, default=_new_id)
    frame_id: Mapped[str | None] = mapped_column(String(36), nullable=True)
    upload_id: Mapped[str | None] = mapped_column(String(36), nullable=True, unique=True)
    telescope_id: Mapped[str] = mapped_column(String(128), nullable=False)
    task_id: Mapped[str | None] = mapped_column(String(64), nullable=True)
    campaign_id: Mapped[str | None] = mapped_column(String(128), nullable=True)
    object_key: Mapped[str] = mapped_column(String(1024), nullable=False)
    checksum_sha256: Mapped[str | None] = mapped_column(String(64), nullable=True)
    date_obs: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)
    mjd_obs: Mapped[float | None] = mapped_column(Float, nullable=True)
    ra_deg: Mapped[float | None] = mapped_column(Float, nullable=True)
    dec_deg: Mapped[float | None] = mapped_column(Float, nullable=True)
    filter: Mapped[str | None] = mapped_column(String(64), nullable=True)
    exptime_sec: Mapped[float | None] = mapped_column(Float, nullable=True)
    airmass: Mapped[float | None] = mapped_column(Float, nullable=True)
    fwhm_arcsec: Mapped[float | None] = mapped_column(Float, nullable=True)
    snr: Mapped[float | None] = mapped_column(Float, nullable=True)
    tier: Mapped[int | None] = mapped_column(Integer, nullable=True)
    calstat: Mapped[str | None] = mapped_column(String(32), nullable=True)
    phot_flag: Mapped[str | None] = mapped_column(String(16), nullable=True)
    headline_grade: Mapped[int | None] = mapped_column(Integer, nullable=True)
    stack_eligible: Mapped[bool | None] = mapped_column(Boolean, nullable=True)
    grade_json: Mapped[dict | None] = mapped_column(JSON, nullable=True)
    zp: Mapped[float | None] = mapped_column(Float, nullable=True)
    created_at: Mapped[datetime] = mapped_column(
        DateTime(timezone=True), nullable=False, default=_utcnow
    )

    __table_args__ = (
        Index("ix_frame_catalog_sky", "ra_deg", "dec_deg"),
        Index("ix_frame_catalog_time", "date_obs"),
        Index("ix_frame_catalog_tele_filter", "telescope_id", "filter"),
        Index("ix_frame_catalog_campaign", "campaign_id"),
    )
