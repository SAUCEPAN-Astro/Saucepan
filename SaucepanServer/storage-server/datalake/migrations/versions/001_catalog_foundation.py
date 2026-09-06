"""Initial catalog schema — uploads, frames, and local processing jobs."""

from typing import Sequence, Union

from alembic import op
import sqlalchemy as sa

revision: str = "001_catalog_foundation"
down_revision: Union[str, None] = None
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    op.create_table(
        "uploads",
        sa.Column("id", sa.String(length=36), nullable=False),
        sa.Column("status", sa.String(length=32), nullable=False),
        sa.Column("bucket", sa.String(length=255), nullable=False),
        sa.Column("object_key", sa.String(length=1024), nullable=False),
        sa.Column("filename", sa.String(length=512), nullable=False),
        sa.Column("campaign_id", sa.String(length=128), nullable=False),
        sa.Column("task_id", sa.String(length=64), nullable=True),
        sa.Column("telescope_id", sa.String(length=128), nullable=True),
        sa.Column("content_type", sa.String(length=128), nullable=False),
        sa.Column("size_bytes", sa.BigInteger(), nullable=True),
        sa.Column("etag", sa.String(length=128), nullable=True),
        sa.Column("metadata_json", sa.JSON(), nullable=True),
        sa.Column("created_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("completed_at", sa.DateTime(timezone=True), nullable=True),
        sa.PrimaryKeyConstraint("id"),
    )
    op.create_index("ix_uploads_status", "uploads", ["status"], unique=False)
    op.create_index("ix_uploads_object_key", "uploads", ["object_key"], unique=False)

    op.create_table(
        "frames",
        sa.Column("id", sa.String(length=36), nullable=False),
        sa.Column("upload_id", sa.String(length=36), nullable=False),
        sa.Column("campaign_id", sa.String(length=128), nullable=False),
        sa.Column("object_key", sa.String(length=1024), nullable=False),
        sa.Column("staged_path", sa.String(length=2048), nullable=True),
        sa.Column("checksum_sha256", sa.String(length=64), nullable=True),
        sa.Column("size_bytes", sa.BigInteger(), nullable=True),
        sa.Column("grade_status", sa.String(length=64), nullable=True),
        sa.Column("created_at", sa.DateTime(timezone=True), nullable=False),
        sa.ForeignKeyConstraint(["upload_id"], ["uploads.id"]),
        sa.PrimaryKeyConstraint("id"),
    )
    op.create_index("ux_frames_upload_id", "frames", ["upload_id"], unique=True)

    op.create_table(
        "jobs",
        sa.Column("id", sa.String(length=36), nullable=False),
        sa.Column("job_type", sa.String(length=64), nullable=False),
        sa.Column("frame_id", sa.String(length=36), nullable=True),
        sa.Column("payload_json", sa.JSON(), nullable=True),
        sa.Column("status", sa.String(length=32), nullable=False),
        sa.Column("error_message", sa.Text(), nullable=True),
        sa.Column("created_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("started_at", sa.DateTime(timezone=True), nullable=True),
        sa.Column("completed_at", sa.DateTime(timezone=True), nullable=True),
        sa.ForeignKeyConstraint(["frame_id"], ["frames.id"]),
        sa.PrimaryKeyConstraint("id"),
    )
    op.create_index("ix_jobs_status", "jobs", ["status"], unique=False)
    op.create_index("ix_jobs_job_type", "jobs", ["job_type"], unique=False)


def downgrade() -> None:
    op.drop_index("ix_jobs_job_type", table_name="jobs")
    op.drop_index("ix_jobs_status", table_name="jobs")
    op.drop_table("jobs")
    op.drop_index("ux_frames_upload_id", table_name="frames")
    op.drop_table("frames")
    op.drop_index("ix_uploads_object_key", table_name="uploads")
    op.drop_index("ix_uploads_status", table_name="uploads")
    op.drop_table("uploads")
