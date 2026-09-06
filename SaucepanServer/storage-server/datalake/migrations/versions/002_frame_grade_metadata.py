"""Add grade metadata columns to frames (sync grading path)."""

from typing import Sequence, Union

from alembic import op
import sqlalchemy as sa

revision: str = "002_frame_grade_metadata"
down_revision: Union[str, None] = "001_catalog_foundation"
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    op.add_column("frames", sa.Column("headline_grade", sa.Integer(), nullable=True))
    op.add_column("frames", sa.Column("grade_json", sa.JSON(), nullable=True))
    op.add_column(
        "frames",
        sa.Column("ingest_status", sa.String(length=32), nullable=True),
    )


def downgrade() -> None:
    op.drop_column("frames", "ingest_status")
    op.drop_column("frames", "grade_json")
    op.drop_column("frames", "headline_grade")
