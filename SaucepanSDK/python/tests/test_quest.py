"""Tests for QuestClient — dev-path task submission to the task server."""

from unittest.mock import Mock, patch

import pytest
import requests

from saucepan.exceptions import ServerError, ValidationError
from saucepan.models import TaskSpec
from saucepan.quest import QuestClient


@pytest.fixture
def quest():
    return QuestClient("http://127.0.0.1:8080")


class TestHealth:
    def test_health_ok(self, quest):
        with patch("saucepan.quest.requests.get", return_value=Mock(ok=True)):
            assert quest.health() is True

    def test_health_not_ok(self, quest):
        with patch("saucepan.quest.requests.get", return_value=Mock(ok=False)):
            assert quest.health() is False

    def test_health_connection_error_returns_false(self, quest):
        with patch("saucepan.quest.requests.get", side_effect=requests.ConnectionError("down")):
            assert quest.health() is False


class TestSubmit:
    def test_submit_validates_spec_first(self, quest):
        bad_spec = TaskSpec(name="", integration_time=300, min_power=0.7)
        with patch("saucepan.quest.requests.post") as mock_post:
            with pytest.raises(ValidationError):
                quest.submit(bad_spec)
        mock_post.assert_not_called()

    def test_submit_success(self, quest):
        spec = TaskSpec(name="M42", integration_time=300, min_power=0.7)
        resp = Mock(status_code=201)
        resp.json.return_value = {"task": {"id": "abc-123"}}
        with patch("saucepan.quest.requests.post", return_value=resp) as mock_post:
            out = quest.submit(spec)

        assert out == {"id": "abc-123", "name": "M42"}
        payload = mock_post.call_args.kwargs["json"]
        assert payload["allow_emulator"] is False

    def test_submit_includes_optional_targeting_fields(self, quest):
        spec = TaskSpec(name="M42", integration_time=300, min_power=0.7)
        resp = Mock(status_code=200)
        resp.json.return_value = {"task": {"id": "abc"}}
        with patch("saucepan.quest.requests.post", return_value=resp) as mock_post:
            quest.submit(
                spec,
                target_ra=359.999,
                target_dec=89.999,
                min_altitude_deg=30.0,
                allow_emulator=True,
            )

        payload = mock_post.call_args.kwargs["json"]
        assert payload["target_ra"] == 359.999
        assert payload["target_dec"] == 89.999
        assert payload["min_altitude_deg"] == 30.0
        assert payload["allow_emulator"] is True

    def test_submit_connection_error_raises_server_error(self, quest):
        spec = TaskSpec(name="M42", integration_time=300, min_power=0.7)
        with patch("saucepan.quest.requests.post", side_effect=requests.ConnectionError("down")):
            with pytest.raises(ServerError, match="Connection failed"):
                quest.submit(spec)

    def test_submit_non_2xx_raises_server_error_with_body_error(self, quest):
        spec = TaskSpec(name="M42", integration_time=300, min_power=0.7)
        resp = Mock(status_code=422)
        resp.json.return_value = {"error": "bad target"}
        with patch("saucepan.quest.requests.post", return_value=resp):
            with pytest.raises(ServerError, match="bad target"):
                quest.submit(spec)

    def test_submit_non_2xx_with_unparseable_body_uses_default_message(self, quest):
        spec = TaskSpec(name="M42", integration_time=300, min_power=0.7)
        resp = Mock(status_code=500)
        resp.json.side_effect = ValueError("not json")
        with patch("saucepan.quest.requests.post", return_value=resp):
            with pytest.raises(ServerError, match="Unexpected HTTP 500"):
                quest.submit(spec)

    def test_submit_missing_task_id_raises_server_error(self, quest):
        spec = TaskSpec(name="M42", integration_time=300, min_power=0.7)
        resp = Mock(status_code=201)
        resp.json.return_value = {"task": {}}
        with patch("saucepan.quest.requests.post", return_value=resp):
            with pytest.raises(ServerError, match="missing task.id"):
                quest.submit(spec)

    def test_submit_boundary_min_power_zero(self, quest):
        spec = TaskSpec(name="M42", integration_time=1, min_power=0.0)
        resp = Mock(status_code=200)
        resp.json.return_value = {"task": {"id": "abc"}}
        with patch("saucepan.quest.requests.post", return_value=resp):
            out = quest.submit(spec)  # should not raise — 0.0 is a valid boundary
        assert out["id"] == "abc"

    def test_submit_boundary_min_power_one(self, quest):
        spec = TaskSpec(name="M42", integration_time=1, min_power=1.0)
        resp = Mock(status_code=200)
        resp.json.return_value = {"task": {"id": "abc"}}
        with patch("saucepan.quest.requests.post", return_value=resp):
            out = quest.submit(spec)  # should not raise — 1.0 is a valid boundary
        assert out["id"] == "abc"


class TestGet:
    def test_get_success(self, quest):
        resp = Mock(ok=True)
        resp.json.return_value = {"task": {"id": "abc", "status": "pending"}}
        with patch("saucepan.quest.requests.get", return_value=resp) as mock_get:
            out = quest.get("abc")

        assert out == {"id": "abc", "status": "pending"}
        assert mock_get.call_args.args[0].endswith("/quest/tasks/abc")

    def test_get_not_ok_raises_server_error(self, quest):
        resp = Mock(ok=False, status_code=404)
        with patch("saucepan.quest.requests.get", return_value=resp):
            with pytest.raises(ServerError, match="HTTP 404"):
                quest.get("missing")

    def test_get_connection_error_raises_server_error(self, quest):
        with patch("saucepan.quest.requests.get", side_effect=requests.ConnectionError("down")):
            with pytest.raises(ServerError, match="Connection failed"):
                quest.get("abc")

    def test_get_returns_none_when_task_key_missing(self, quest):
        resp = Mock(ok=True)
        resp.json.return_value = {}
        with patch("saucepan.quest.requests.get", return_value=resp):
            assert quest.get("abc") is None
