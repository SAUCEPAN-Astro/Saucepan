"""Direct unit tests for auth.py bearer-token helpers (no Flask app needed)."""

from __future__ import annotations

import pytest
from flask import Flask

import auth


@pytest.fixture(autouse=True)
def _clear_auth_env(monkeypatch):
    monkeypatch.delenv("DATALAKE_GRADING_TOKEN", raising=False)
    monkeypatch.delenv("DATALAKE_ALLOW_INSECURE", raising=False)
    monkeypatch.delenv("FLASK_ENV", raising=False)


def test_allow_insecure_via_flag(monkeypatch):
    monkeypatch.setenv("DATALAKE_ALLOW_INSECURE", "1")
    assert auth._allow_insecure() is True


def test_allow_insecure_via_flask_env(monkeypatch):
    monkeypatch.setenv("FLASK_ENV", "development")
    assert auth._allow_insecure() is True


def test_allow_insecure_false_by_default():
    assert auth._allow_insecure() is False


def test_allow_insecure_ignores_non_1_value(monkeypatch):
    monkeypatch.setenv("DATALAKE_ALLOW_INSECURE", "true")
    assert auth._allow_insecure() is False


def test_expected_token_strips_whitespace(monkeypatch):
    monkeypatch.setenv("DATALAKE_GRADING_TOKEN", "  secret  ")
    assert auth._expected_token() == "secret"


def test_check_grading_token_no_token_configured_insecure_ok(monkeypatch):
    monkeypatch.setenv("DATALAKE_ALLOW_INSECURE", "1")
    app = Flask(__name__)
    with app.test_request_context("/"):
        assert auth.check_grading_token() is None


def test_check_grading_token_no_token_configured_fails_closed():
    app = Flask(__name__)
    with app.test_request_context("/"):
        resp, status = auth.check_grading_token()
        assert status == 401


def test_check_grading_token_wrong_bearer(monkeypatch):
    monkeypatch.setenv("DATALAKE_GRADING_TOKEN", "secret")
    app = Flask(__name__)
    with app.test_request_context("/", headers={"Authorization": "Bearer wrong"}):
        resp, status = auth.check_grading_token()
        assert status == 401


def test_check_grading_token_missing_header(monkeypatch):
    monkeypatch.setenv("DATALAKE_GRADING_TOKEN", "secret")
    app = Flask(__name__)
    with app.test_request_context("/"):
        resp, status = auth.check_grading_token()
        assert status == 401


def test_check_grading_token_correct_bearer_passes(monkeypatch):
    monkeypatch.setenv("DATALAKE_GRADING_TOKEN", "secret")
    app = Flask(__name__)
    with app.test_request_context("/", headers={"Authorization": "Bearer secret"}):
        assert auth.check_grading_token() is None


def test_require_grading_token_decorator_blocks(monkeypatch):
    monkeypatch.setenv("DATALAKE_GRADING_TOKEN", "secret")
    app = Flask(__name__)

    @auth.require_grading_token
    def handler():
        return "ok"

    with app.test_request_context("/"):
        result = handler()
        assert isinstance(result, tuple)
        assert result[1] == 401


def test_require_grading_token_decorator_allows(monkeypatch):
    monkeypatch.setenv("DATALAKE_GRADING_TOKEN", "secret")
    app = Flask(__name__)

    @auth.require_grading_token
    def handler():
        return "ok"

    with app.test_request_context("/", headers={"Authorization": "Bearer secret"}):
        assert handler() == "ok"


def test_ensure_grading_token_at_startup_exits_when_unset(monkeypatch):
    with pytest.raises(SystemExit):
        auth.ensure_grading_token_at_startup()


def test_ensure_grading_token_at_startup_ok_with_token(monkeypatch):
    monkeypatch.setenv("DATALAKE_GRADING_TOKEN", "secret")
    auth.ensure_grading_token_at_startup()  # must not raise


def test_ensure_grading_token_at_startup_ok_when_insecure(monkeypatch):
    monkeypatch.setenv("DATALAKE_ALLOW_INSECURE", "1")
    auth.ensure_grading_token_at_startup()  # must not raise, only warns
