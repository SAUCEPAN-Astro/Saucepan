"""Denormalized L1 frame catalog index (sky + time) on local SQLite."""

from alembic import op
import sqlalchemy as sa

revision = "004_frame_catalog"
down_revision = "003_metric_observations"
branch_labels = None
depends_on = None


def upgrade() -> None:
    op.create_table(
        "frame_catalog",
        sa.Column("id", sa.String(36), primary_key=True),
        sa.Column("frame_id", sa.String(36), nullable=True),
        sa.Column("upload_id", sa.String(36), nullable=True),
        sa.Column("telescope_id", sa.String(128), nullable=False),
        sa.Column("task_id", sa.String(64), nullable=True),
        sa.Column("campaign_id", sa.String(128), nullable=True),
        sa.Column("object_key", sa.String(1024), nullable=False),
        sa.Column("checksum_sha256", sa.String(64), nullable=True),
        sa.Column("date_obs", sa.DateTime(timezone=True), nullable=True),
        sa.Column("mjd_obs", sa.Float(), nullable=True),
        sa.Column("ra_deg", sa.Float(), nullable=True),
        sa.Column("dec_deg", sa.Float(), nullable=True),
        sa.Column("filter", sa.String(64), nullable=True),
        sa.Column("exptime_sec", sa.Float(), nullable=True),
        sa.Column("airmass", sa.Float(), nullable=True),
        sa.Column("fwhm_arcsec", sa.Float(), nullable=True),
        sa.Column("snr", sa.Float(), nullable=True),
        sa.Column("tier", sa.Integer(), nullable=True),
        sa.Column("calstat", sa.String(32), nullable=True),
        sa.Column("phot_flag", sa.String(16), nullable=True),
        sa.Column("headline_grade", sa.Integer(), nullable=True),
        sa.Column("stack_eligible", sa.Boolean(), nullable=True),
        sa.Column("grade_json", sa.JSON(), nullable=True),
        sa.Column("zp", sa.Float(), nullable=True),
        sa.Column(
            "created_at",
            sa.DateTime(timezone=True),
            server_default=sa.text("CURRENT_TIMESTAMP"),
            nullable=False,
        ),
        sa.UniqueConstraint("upload_id", name="uq_frame_catalog_upload_id"),
    )
    op.create_index("ix_frame_catalog_sky", "frame_catalog", ["ra_deg", "dec_deg"])
    op.create_index("ix_frame_catalog_time", "frame_catalog", ["date_obs"])
    op.create_index(
        "ix_frame_catalog_tele_filter", "frame_catalog", ["telescope_id", "filter"]
    )
    op.create_index("ix_frame_catalog_campaign", "frame_catalog", ["campaign_id"])


def downgrade() -> None:
    op.drop_index("ix_frame_catalog_campaign", table_name="frame_catalog")
    op.drop_index("ix_frame_catalog_tele_filter", table_name="frame_catalog")
    op.drop_index("ix_frame_catalog_time", table_name="frame_catalog")
    op.drop_index("ix_frame_catalog_sky", table_name="frame_catalog")
    op.drop_table("frame_catalog")
