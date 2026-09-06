"""Shared pytest helpers for datalake upload auth tests."""

from __future__ import annotations

import pytest

TEST_GRADING_TOKEN = "test-grading-token"


@pytest.fixture(autouse=True)
def _datalake_boot_safe_for_tests(monkeypatch):
    """create_app() calls ensure_grading_token_at_startup — don't SystemExit the suite."""
    monkeypatch.setenv("DATALAKE_ALLOW_INSECURE", "1")


@pytest.fixture()
def grading_token_env(monkeypatch):
    monkeypatch.setenv("DATALAKE_GRADING_TOKEN", TEST_GRADING_TOKEN)


@pytest.fixture()
def auth_headers() -> dict[str, str]:
    return {"Authorization": f"Bearer {TEST_GRADING_TOKEN}"}
