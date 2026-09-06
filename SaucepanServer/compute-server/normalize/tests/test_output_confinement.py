import sys
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))

from normalize.normalize import _resolve_output_path


def test_output_path_rejects_parent_segments_without_storage_root() -> None:
    with pytest.raises(ValueError, match="must not contain"):
        _resolve_output_path("../escape.fits", base_dir="")


def test_output_path_is_confined_to_base_dir(tmp_path: Path) -> None:
    assert _resolve_output_path("nested/out.fits", base_dir=str(tmp_path)) == (
        tmp_path / "nested" / "out.fits"
    )
    with pytest.raises(ValueError, match="outside"):
        _resolve_output_path(str(tmp_path.parent / "escape.fits"), base_dir=str(tmp_path))
