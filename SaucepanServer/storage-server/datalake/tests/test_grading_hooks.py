"""Unit tests for grading_hooks.py — the pluggable post-upload grading hook registry."""

from __future__ import annotations

from unittest.mock import patch

import pytest

import grading_hooks


@pytest.fixture(autouse=True)
def _reset_hook():
    original = grading_hooks._on_upload_complete
    yield
    grading_hooks._on_upload_complete = original


def test_register_and_invoke_custom_hook():
    calls = []

    def custom_hook(upload_id: str) -> str:
        calls.append(upload_id)
        return "custom_status"

    grading_hooks.register_on_upload_complete(custom_hook)
    result = grading_hooks.on_upload_complete("upload-1")
    assert result == "custom_status"
    assert calls == ["upload-1"]


def test_on_upload_complete_defaults_to_run_post_upload_grading_for_upload():
    # Import routes.upload.grading *before* clearing the hook and patching:
    # importing routes.upload (the parent package) as a side effect re-runs
    # `register_on_upload_complete(run_post_upload_grading_for_upload)` at
    # module scope, which would silently re-populate _on_upload_complete
    # with the *unpatched* function if the import happened inside the
    # `with patch(...)` block below (first import wins the reference).
    import routes.upload.grading  # noqa: F401

    grading_hooks._on_upload_complete = None
    with patch(
        "routes.upload.grading.run_post_upload_grading_for_upload",
        return_value="default_status",
    ) as mock_fn:
        result = grading_hooks.on_upload_complete("upload-2")
    assert result == "default_status"
    mock_fn.assert_called_once_with("upload-2")


def test_grading_result_to_dict():
    assert grading_hooks.grading_result_to_dict("grade_assessed") == {
        "pipeline_status": "grade_assessed"
    }
