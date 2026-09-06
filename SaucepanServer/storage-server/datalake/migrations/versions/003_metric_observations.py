"""Add metric_observations table for sidecar storage."""

from alembic import op
import sqlalchemy as sa


revision = "003_metric_observations"
down_revision = "002_frame_grade_metadata"
branch_labels = None
depends_on = None


def upgrade() -> None:
    op.create_table(
        "metric_observations",
        sa.Column("id", sa.String(36), primary_key=True),
        sa.Column("upload_id", sa.String(36), nullable=True),
        sa.Column("frame_id", sa.String(36), nullable=True),
        sa.Column("telescope_id", sa.String(128), nullable=True),
        sa.Column("node_id", sa.String(128), nullable=True),
        sa.Column("producer", sa.String(64), nullable=False),
        sa.Column("observed_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("metrics_json", sa.Text(), nullable=False),
        sa.Column("context_json", sa.Text(), nullable=False),
        sa.Column("wait_pile_json", sa.Text(), nullable=False, server_default="[]"),
    )
    op.create_index("ix_metric_obs_upload", "metric_observations", ["upload_id"])
    op.create_index("ix_metric_obs_tele", "metric_observations", ["telescope_id"])


def downgrade() -> None:
    op.drop_index("ix_metric_obs_tele", table_name="metric_observations")
    op.drop_index("ix_metric_obs_upload", table_name="metric_observations")
    op.drop_table("metric_observations")
