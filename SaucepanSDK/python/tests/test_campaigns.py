"""Tests for campaign authoring client."""

import threading
from unittest.mock import Mock, patch

import pytest

from saucepan.campaigns import (
    CampaignClient,
    CampaignPack,
    CampaignTask,
    ResearcherSession,
    TextInbox,
    _normalize_pack,
    _opt_float,
    _safe_json,
    login_researcher,
    login_researcher_session,
    refresh_researcher_session,
)
from saucepan.exceptions import AuthError, ServerError, ValidationError


class TestCampaignTask:
    def test_budget_progress(self):
        t = CampaignTask(
            id="t1",
            name="M31",
            status="pending",
            normalized_integration_budget_s=100.0,
            normalized_integration_earned_s=40.0,
        )
        assert t.budget_remaining == 60.0
        assert t.pct_complete == 40.0
        assert not t.is_budget_complete

    def test_from_dict(self):
        t = CampaignTask.from_dict(
            {
                "id": "uuid-1",
                "name": "x",
                "status": "completed",
                "normalized_integration_budget_s": 10,
                "normalized_integration_earned_s": 10,
                "assigned_telescope_id": "scope-a",
            },
            campaign_id="c1",
        )
        assert t.campaign_id == "c1"
        assert t.assigned_telescope_id == "scope-a"
        assert t.is_budget_complete


class TestCampaignPack:
    def test_validate_publish_requires_targets(self):
        pack = CampaignPack(name="x", targets=[])
        with pytest.raises(ValidationError):
            pack.validate(for_publish=True)

    def test_validate_publish_demo_shape(self):
        pack = CampaignPack(
            name="demo",
            test_only=True,
            targets=[
                {
                    "ra": 10.0,
                    "dec": 41.0,
                    "filters": ["R"],
                    "exposure_sec": 20,
                    "frame_count": 3,
                }
            ],
        )
        pack.validate(for_publish=True)


class TestCampaignClient:
    @pytest.fixture
    def client(self):
        return CampaignClient("https://api.example.test", access_token="tok")

    @patch("saucepan.campaigns.requests.request")
    def test_create(self, mock_request, client):
        mock_resp = Mock()
        mock_resp.status_code = 201
        mock_resp.content = b'{"campaign":{"id":"abc-123","status":"draft"}}'
        mock_resp.json.return_value = {"campaign": {"id": "abc-123", "status": "draft"}}
        mock_request.return_value = mock_resp

        pack = CampaignPack(name="demo", targets=[{"ra": 1, "dec": 2}])
        out = client.create(pack)
        assert out["id"] == "abc-123"
        mock_request.assert_called_once()
        call = mock_request.call_args
        assert call.kwargs["json"]["name"] == "demo"
        assert call.args[0] == "POST"
        assert "/api/v1/campaigns" in call.args[1]

    @patch("saucepan.campaigns.requests.request")
    def test_publish(self, mock_request, client):
        mock_resp = Mock()
        mock_resp.status_code = 200
        mock_resp.content = b'{"campaign":{"id":"abc","status":"active","tasks_created":2}}'
        mock_resp.json.return_value = {
            "campaign": {"id": "abc", "status": "active", "tasks_created": 2}
        }
        mock_request.return_value = mock_resp

        out = client.publish("abc")
        assert out["tasks_created"] == 2
        assert "/api/v1/campaigns/abc/publish" in mock_request.call_args.args[1]

    @patch("saucepan.campaigns.requests.request")
    def test_list_tasks(self, mock_request, client):
        mock_resp = Mock()
        mock_resp.status_code = 200
        mock_resp.content = b'{"tasks":[{"id":"t1"}]}'
        mock_resp.json.return_value = {"tasks": [{"id": "t1"}]}
        mock_request.return_value = mock_resp

        tasks = client.list_tasks("abc")
        assert len(tasks) == 1
        assert tasks[0].id == "t1"

    @patch("saucepan.campaigns.requests.request")
    def test_pause_resume(self, mock_request, client):
        mock_resp = Mock()
        mock_resp.status_code = 200
        mock_resp.content = b'{"campaign":{"id":"abc","status":"paused"}}'
        mock_resp.json.return_value = {"campaign": {"id": "abc", "status": "paused"}}
        mock_request.return_value = mock_resp

        out = client.pause("abc")
        assert out["status"] == "paused"
        assert "/pause" in mock_request.call_args.args[1]

        mock_resp.json.return_value = {"campaign": {"id": "abc", "status": "active"}}
        out = client.resume("abc")
        assert out["status"] == "active"

    @patch("saucepan.campaigns.requests.request")
    def test_stack_status(self, mock_request, client):
        mock_resp = Mock()
        mock_resp.status_code = 200
        mock_resp.content = b'{"campaign_id":"abc","tasks":[]}'
        mock_resp.json.return_value = {"campaign_id": "abc", "tasks": []}
        mock_request.return_value = mock_resp

        out = client.stack_status("abc")
        assert out["campaign_id"] == "abc"
        assert "/stack-status" in mock_request.call_args.args[1]

    @patch("saucepan.campaigns.requests.request")
    def test_alerts_poll(self, mock_request, client):
        mock_resp = Mock()
        mock_resp.status_code = 200
        mock_resp.content = b'{"alerts":[{"id":"a1","message":"review"}]}'
        mock_resp.json.return_value = {"alerts": [{"id": "a1", "message": "review"}]}
        mock_request.return_value = mock_resp

        alerts = client.alerts.poll(campaign_id="c1")
        assert alerts[0]["id"] == "a1"
        assert (
            "campaign_id=c1" in mock_request.call_args.kwargs["params"].values()
            or mock_request.call_args.kwargs["params"].get("campaign_id") == "c1"
        )

    @patch("saucepan.campaigns.requests.request")
    def test_account_usage(self, mock_request, client):
        mock_resp = Mock()
        mock_resp.status_code = 200
        mock_resp.content = b'{"totals":{"frames_graded":2},"note":"tracker"}'
        mock_resp.json.return_value = {
            "totals": {"frames_graded": 2},
            "campaigns": [],
            "note": "tracker",
        }
        mock_request.return_value = mock_resp

        out = client.account_usage(since="2026-01-01T00:00:00Z")
        assert out["totals"]["frames_graded"] == 2
        assert "/api/v1/account/usage" in mock_request.call_args.args[1]
        assert mock_request.call_args.kwargs["params"]["since"] == "2026-01-01T00:00:00Z"

    def test_requires_token(self, client):
        client.access_token = ""
        with pytest.raises(AuthError):
            client.get("x")


class TestLoginResearcher:
    def test_session_repr_hides_tokens_and_email(self):
        session = ResearcherSession(
            access_token="access-secret",
            refresh_token="refresh-secret",
            email="test-user",
        )
        rendered = repr(session)
        assert "access-secret" not in rendered
        assert "refresh-secret" not in rendered
        assert "test-user" not in rendered

    @patch("saucepan.campaigns.requests.post")
    def test_login_success(self, mock_post):
        mock_resp = Mock()
        mock_resp.ok = True
        mock_resp.status_code = 200
        mock_resp.json.return_value = {"access_token": "jwt-here"}
        mock_post.return_value = mock_resp

        token = login_researcher("https://api.example.test", "alice", "secret")
        assert token == "jwt-here"
        mock_post.assert_called_once()
        assert mock_post.call_args.kwargs["json"] == {
            "username": "alice",
            "password": "secret",
        }

    @patch("saucepan.campaigns.requests.post")
    def test_login_forbidden(self, mock_post):
        mock_resp = Mock()
        mock_resp.ok = False
        mock_resp.status_code = 403
        mock_resp.json.return_value = {"error": "researcher approval required"}
        mock_post.return_value = mock_resp

        with pytest.raises(AuthError):
            login_researcher("https://api.example.test", "alice", "secret")

    @patch("saucepan.campaigns.requests.post")
    def test_login_invalid_credentials_401(self, mock_post):
        mock_resp = Mock(ok=False, status_code=401)
        mock_post.return_value = mock_resp

        with pytest.raises(AuthError, match="Invalid username or password"):
            login_researcher_session("https://api.example.test", "alice", "wrong")

    @patch("saucepan.campaigns.requests.post")
    def test_login_server_error(self, mock_post):
        mock_resp = Mock(ok=False, status_code=500)
        mock_resp.json.return_value = {"error": "boom"}
        mock_post.return_value = mock_resp

        with pytest.raises(ServerError):
            login_researcher_session("https://api.example.test", "alice", "secret")

    @patch("saucepan.campaigns.requests.post")
    def test_login_missing_access_token_raises(self, mock_post):
        mock_resp = Mock(ok=True, status_code=200)
        mock_resp.json.return_value = {"user": {"id": "u1"}}
        mock_post.return_value = mock_resp

        with pytest.raises(ServerError, match="missing access_token"):
            login_researcher_session("https://api.example.test", "alice", "secret")

    @patch(
        "saucepan.campaigns.requests.post",
        side_effect=__import__("requests").ConnectionError("down"),
    )
    def test_login_connection_error(self, mock_post):
        with pytest.raises(ServerError, match="Connection failed"):
            login_researcher_session("https://api.example.test", "alice", "secret")

    @patch("saucepan.campaigns.requests.post")
    def test_login_session_full_fields(self, mock_post):
        mock_resp = Mock(ok=True, status_code=200)
        mock_resp.json.return_value = {
            "access_token": "jwt",
            "refresh_token": "rjwt",
            "expires_at": "2026-12-01T00:00:00Z",
            "user": {"id": "u1", "email": "test-user"},
        }
        mock_post.return_value = mock_resp

        session = login_researcher_session("https://api.example.test", "alice", "secret")
        assert session.access_token == "jwt"
        assert session.refresh_token == "rjwt"
        assert session.user_id == "u1"
        assert session.email == "test-user"


class TestRefreshResearcherSession:
    def test_empty_refresh_token_raises(self):
        with pytest.raises(AuthError):
            refresh_researcher_session("https://api.example.test", "")

    @patch("saucepan.campaigns.requests.post")
    def test_refresh_401_raises_auth_error(self, mock_post):
        mock_resp = Mock(ok=False, status_code=401)
        mock_resp.json.return_value = {"error": "expired"}
        mock_post.return_value = mock_resp

        with pytest.raises(AuthError):
            refresh_researcher_session("https://api.example.test", "old-refresh")

    @patch("saucepan.campaigns.requests.post")
    def test_refresh_server_error(self, mock_post):
        mock_resp = Mock(ok=False, status_code=500)
        mock_resp.json.return_value = {"error": "boom"}
        mock_post.return_value = mock_resp

        with pytest.raises(ServerError):
            refresh_researcher_session("https://api.example.test", "old-refresh")

    @patch("saucepan.campaigns.requests.post")
    def test_refresh_missing_access_token_raises(self, mock_post):
        mock_resp = Mock(ok=True, status_code=200)
        mock_resp.json.return_value = {}
        mock_post.return_value = mock_resp

        with pytest.raises(ServerError, match="missing access_token"):
            refresh_researcher_session("https://api.example.test", "old-refresh")

    @patch("saucepan.campaigns.requests.post")
    def test_refresh_keeps_old_refresh_token_if_none_returned(self, mock_post):
        mock_resp = Mock(ok=True, status_code=200)
        mock_resp.json.return_value = {"access_token": "new-jwt"}
        mock_post.return_value = mock_resp

        session = refresh_researcher_session("https://api.example.test", "old-refresh")
        assert session.access_token == "new-jwt"
        assert session.refresh_token == "old-refresh"


class TestCampaignPackConstraints:
    def test_coverage_mode_invalid(self):
        pack = CampaignPack(name="x", coverage={"mode": "bogus"})
        with pytest.raises(ValidationError) as exc:
            pack.validate()
        assert "coverage.mode" in exc.value.fields

    def test_coverage_mode_valid_hard(self):
        pack = CampaignPack(name="x", coverage={"mode": "hard"})
        pack.validate()  # should not raise

    def test_coverage_longitude_span_lower_boundary(self):
        pack = CampaignPack(name="x", coverage={"min_longitude_span_deg": 0})
        pack.validate()

    def test_coverage_longitude_span_upper_boundary(self):
        pack = CampaignPack(name="x", coverage={"min_longitude_span_deg": 360})
        pack.validate()

    def test_coverage_longitude_span_below_range(self):
        pack = CampaignPack(name="x", coverage={"min_longitude_span_deg": -1})
        with pytest.raises(ValidationError) as exc:
            pack.validate()
        assert "coverage.min_longitude_span_deg" in exc.value.fields

    def test_coverage_longitude_span_above_range(self):
        pack = CampaignPack(name="x", coverage={"min_longitude_span_deg": 361})
        with pytest.raises(ValidationError) as exc:
            pack.validate()
        assert "coverage.min_longitude_span_deg" in exc.value.fields

    def test_season_kind_invalid(self):
        pack = CampaignPack(name="x", season={"kind": "nightly"})
        with pytest.raises(ValidationError) as exc:
            pack.validate()
        assert "season.kind" in exc.value.fields

    @pytest.mark.parametrize("kind", ["continuous", "sparse", "too"])
    def test_season_kind_valid(self, kind):
        pack = CampaignPack(name="x", season={"kind": kind})
        pack.validate()

    def test_season_urgency_invalid(self):
        pack = CampaignPack(name="x", season={"urgency": "panic"})
        with pytest.raises(ValidationError) as exc:
            pack.validate()
        assert "season.urgency" in exc.value.fields

    def test_season_target_duty_cycle_lower_boundary(self):
        pack = CampaignPack(name="x", season={"target_duty_cycle": 0})
        pack.validate()

    def test_season_target_duty_cycle_upper_boundary(self):
        pack = CampaignPack(name="x", season={"target_duty_cycle": 1})
        pack.validate()

    def test_season_target_duty_cycle_below_range(self):
        pack = CampaignPack(name="x", season={"target_duty_cycle": -0.01})
        with pytest.raises(ValidationError) as exc:
            pack.validate()
        assert "season.target_duty_cycle" in exc.value.fields

    def test_season_target_duty_cycle_above_range(self):
        pack = CampaignPack(name="x", season={"target_duty_cycle": 1.01})
        with pytest.raises(ValidationError) as exc:
            pack.validate()
        assert "season.target_duty_cycle" in exc.value.fields

    def test_missing_name(self):
        pack = CampaignPack(name="   ")
        with pytest.raises(ValidationError) as exc:
            pack.validate()
        assert "name" in exc.value.fields

    def test_publish_target_missing_filters(self):
        pack = CampaignPack(
            name="x",
            targets=[{"ra": 1, "dec": 2, "exposure_sec": 10, "frame_count": 3}],
        )
        with pytest.raises(ValidationError) as exc:
            pack.validate(for_publish=True)
        assert "targets[0].filters" in exc.value.fields

    def test_publish_target_exposure_sec_zero(self):
        pack = CampaignPack(
            name="x",
            targets=[{"filters": ["R"], "exposure_sec": 0, "frame_count": 3}],
        )
        with pytest.raises(ValidationError) as exc:
            pack.validate(for_publish=True)
        assert "targets[0].exposure_sec" in exc.value.fields

    def test_publish_target_frame_count_zero(self):
        pack = CampaignPack(
            name="x",
            targets=[{"filters": ["R"], "exposure_sec": 10, "frame_count": 0}],
        )
        with pytest.raises(ValidationError) as exc:
            pack.validate(for_publish=True)
        assert "targets[0].frame_count" in exc.value.fields

    def test_publish_target_saturation_strategy_invalid(self):
        pack = CampaignPack(
            name="x",
            targets=[
                {
                    "filters": ["R"],
                    "exposure_sec": 10,
                    "frame_count": 3,
                    "saturation": {"strategy": "yolo"},
                }
            ],
        )
        with pytest.raises(ValidationError) as exc:
            pack.validate(for_publish=True)
        assert "targets[0].saturation.strategy" in exc.value.fields

    @pytest.mark.parametrize("strategy", ["short", "defocus", "nd", "manual"])
    def test_publish_target_saturation_strategy_valid(self, strategy):
        pack = CampaignPack(
            name="x",
            targets=[
                {
                    "filters": ["R"],
                    "exposure_sec": 10,
                    "frame_count": 3,
                    "saturation": {"strategy": strategy},
                }
            ],
        )
        pack.validate(for_publish=True)

    def test_publish_target_exposure_exceeds_saturation_max(self):
        pack = CampaignPack(
            name="x",
            targets=[
                {
                    "filters": ["R"],
                    "exposure_sec": 30,
                    "frame_count": 3,
                    "saturation": {"max_exposure_sec": 20},
                }
            ],
        )
        with pytest.raises(ValidationError) as exc:
            pack.validate(for_publish=True)
        assert "targets[0].exposure_sec" in exc.value.fields

    def test_from_dict_and_to_dict_roundtrip_with_optional_fields(self):
        data = {
            "name": "demo",
            "targets": [{"ra": 1, "dec": 2}],
            "test_only": True,
            "description": "desc",
            "hook_image_ref": "img://x",
            "hook_placement": "before",
            "comp_stars": [{"ra": 3, "dec": 4}],
            "product": {"mode": "time_bin", "time_bin_frames": 10},
            "coverage": {"mode": "hard"},
            "season": {"kind": "sparse"},
            "pier_code": {
                "enabled": True,
                "actions": {"read_frame": True, "next_capture": False},
            },
        }
        pack = CampaignPack.from_dict(data)
        out = pack.to_dict()
        assert out["test_only"] is True
        assert out["description"] == "desc"
        assert out["hook_image_ref"] == "img://x"
        assert out["hook_placement"] == "before"
        assert out["comp_stars"] == [{"ra": 3, "dec": 4}]
        assert out["product"] == {"mode": "time_bin", "time_bin_frames": 10}
        assert out["coverage"] == {"mode": "hard"}
        assert out["season"] == {"kind": "sparse"}
        assert out["pier_code"] == {
            "enabled": True,
            "actions": {"read_frame": True, "next_capture": False},
        }

    def test_to_dict_omits_none_placement_default(self):
        pack = CampaignPack(name="x", targets=[])
        out = pack.to_dict()
        assert "hook_placement" not in out
        assert "test_only" not in out
        assert "coverage" not in out
        assert "season" not in out

    def test_from_json_file(self, tmp_path):
        import json

        p = tmp_path / "pack.json"
        p.write_text(json.dumps({"name": "from-file", "targets": []}))
        pack = CampaignPack.from_json_file(p)
        assert pack.name == "from-file"


class TestCampaignClientErrorPaths:
    @pytest.fixture
    def client(self):
        return CampaignClient("https://api.example.test", access_token="tok", refresh_token="rtok")

    @patch("saucepan.campaigns.requests.request")
    def test_401_with_refresh_token_retries_and_succeeds(self, mock_request, client):
        unauthorized = Mock(status_code=401)
        unauthorized.json.return_value = {"error": "expired"}
        ok_resp = Mock(status_code=200, content=b'{"campaign":{"id":"c1"}}')
        ok_resp.json.return_value = {"campaign": {"id": "c1"}}
        mock_request.side_effect = [unauthorized, ok_resp]

        with patch.object(client, "refresh_auth") as mock_refresh:

            def _refresh():
                client.access_token = "new-tok"

            mock_refresh.side_effect = _refresh
            out = client.get("c1")

        mock_refresh.assert_called_once()
        assert out == {"id": "c1"}
        assert mock_request.call_count == 2

    @patch("saucepan.campaigns.requests.request")
    def test_401_refresh_failure_raises_auth_error(self, mock_request, client):
        unauthorized = Mock(status_code=401)
        unauthorized.json.return_value = {"error": "expired"}
        mock_request.return_value = unauthorized

        with patch.object(client, "refresh_auth", side_effect=AuthError("refresh dead")):
            with pytest.raises(AuthError):
                client.get("c1")

    @patch("saucepan.campaigns.requests.request")
    def test_401_without_refresh_token_raises_directly(self, mock_request):
        client = CampaignClient("https://api.example.test", access_token="tok")
        unauthorized = Mock(status_code=401)
        unauthorized.json.return_value = {"error": "expired"}
        mock_request.return_value = unauthorized

        with pytest.raises(AuthError):
            client.get("c1")

    @patch("saucepan.campaigns.requests.request")
    def test_403_raises_auth_error(self, mock_request, client):
        forbidden = Mock(status_code=403)
        forbidden.json.return_value = {"error": "not your campaign"}
        mock_request.return_value = forbidden

        with pytest.raises(AuthError, match="not your campaign"):
            client.get("c1")

    @patch("saucepan.campaigns.requests.request")
    def test_unexpected_status_raises_server_error(self, mock_request, client):
        resp = Mock(status_code=418)
        resp.json.return_value = {"error": "teapot"}
        mock_request.return_value = resp

        with pytest.raises(ServerError, match="teapot"):
            client.get("c1")

    @patch("saucepan.campaigns.requests.request")
    def test_empty_content_returns_empty_dict(self, mock_request, client):
        resp = Mock(status_code=200, content=b"")
        mock_request.return_value = resp

        out = client._request("POST", "/api/v1/campaigns/c1/pause")
        assert out == {}

    @patch(
        "saucepan.campaigns.requests.request",
        side_effect=__import__("requests").ConnectionError("down"),
    )
    def test_connection_error_raises_server_error(self, mock_request, client):
        with pytest.raises(ServerError, match="Connection failed"):
            client.get("c1")

    @patch("saucepan.campaigns.requests.request")
    def test_create_missing_campaign_id_raises(self, mock_request, client):
        resp = Mock(status_code=201, content=b'{"campaign":{}}')
        resp.json.return_value = {"campaign": {}}
        mock_request.return_value = resp

        with pytest.raises(ServerError, match="missing campaign.id"):
            client.create(CampaignPack(name="x", targets=[]))

    @patch("saucepan.campaigns.requests.request")
    def test_add_task_missing_budget_raises_validation_error(self, mock_request, client):
        from saucepan.models import TaskSpec

        spec = TaskSpec(name="M42", integration_time=300, min_power=0.7)
        with pytest.raises(ValidationError, match="normalized_integration_budget_s"):
            client.add_task("c1", spec)
        mock_request.assert_not_called()

    @patch("saucepan.campaigns.requests.request")
    def test_add_task_missing_task_id_raises_server_error(self, mock_request, client):
        from saucepan.models import TaskSpec

        spec = TaskSpec(
            name="M42",
            integration_time=300,
            min_power=0.7,
            normalized_integration_budget_s=100.0,
        )
        resp = Mock(status_code=201, content=b'{"task":{}}')
        resp.json.return_value = {"task": {}}
        mock_request.return_value = resp

        with pytest.raises(ServerError, match="missing task.id"):
            client.add_task("c1", spec)

    @patch("saucepan.campaigns.requests.request")
    def test_add_task_success_with_ra_dec(self, mock_request, client):
        from saucepan.models import TaskSpec

        spec = TaskSpec(
            name="M42",
            integration_time=300,
            min_power=0.7,
            normalized_integration_budget_s=100.0,
        )
        resp = Mock(status_code=201, content=b'{"task":{"id":"t1","campaign_id":"c1"}}')
        resp.json.return_value = {"task": {"id": "t1", "campaign_id": "c1"}}
        mock_request.return_value = resp

        out = client.add_task("c1", spec, target_ra=180.0, target_dec=-90.0)
        assert out == {"id": "t1", "campaign_id": "c1"}
        payload = mock_request.call_args.kwargs["json"]
        assert payload["target_ra"] == 180.0
        assert payload["target_dec"] == -90.0

    @patch("saucepan.campaigns.requests.request")
    def test_set_coverage_full_params(self, mock_request, client):
        resp = Mock(status_code=200, content=b"{}")
        resp.json.return_value = {}
        mock_request.return_value = resp

        client.set_coverage(
            "c1",
            enabled=True,
            n_main=2,
            redundancy=True,
            max_gap_min=30,
            max_sites=3,
            mode="hard",
            preferred_sites=["site-a"],
            min_sites=2,
            min_longitude_span_deg=90.0,
        )
        payload = mock_request.call_args.kwargs["json"]
        assert payload["max_gap_min"] == 30
        assert payload["max_sites"] == 3
        assert payload["mode"] == "hard"
        assert payload["preferred_sites"] == ["site-a"]
        assert payload["min_sites"] == 2
        assert payload["min_longitude_span_deg"] == 90.0

    @patch("saucepan.campaigns.requests.request")
    def test_preview_coverage_defaults_to_empty_body(self, mock_request, client):
        resp = Mock(status_code=200, content=b"{}")
        resp.json.return_value = {}
        mock_request.return_value = resp

        client.preview_coverage("c1")
        assert mock_request.call_args.kwargs["json"] == {}

    @patch("saucepan.campaigns.requests.request")
    def test_apply_coverage_passes_none_through(self, mock_request, client):
        resp = Mock(status_code=200, content=b"{}")
        resp.json.return_value = {}
        mock_request.return_value = resp

        client.apply_coverage("c1")
        assert mock_request.call_args.kwargs["json"] is None

    @patch("saucepan.campaigns.requests.request")
    def test_coverage_status_with_bin_minutes(self, mock_request, client):
        resp = Mock(status_code=200, content=b"{}")
        resp.json.return_value = {}
        mock_request.return_value = resp

        client.coverage_status("c1", bin_minutes=15.0)
        assert mock_request.call_args.args[1].endswith("/c1/coverage/status")
        assert mock_request.call_args.kwargs["params"] == {"bin_minutes": "15.0"}

    @patch("saucepan.campaigns.requests.request")
    def test_fleet_sites(self, mock_request, client):
        resp = Mock(status_code=200, content=b'{"sites":[]}')
        resp.json.return_value = {"sites": []}
        mock_request.return_value = resp

        out = client.fleet_sites()
        assert out == {"sites": []}
        assert "/api/v1/fleet/sites" in mock_request.call_args.args[1]

    @patch("saucepan.campaigns.requests.request")
    def test_get_returns_none_when_campaign_missing(self, mock_request, client):
        resp = Mock(status_code=200, content=b"{}")
        resp.json.return_value = {}
        mock_request.return_value = resp

        assert client.get("c1") is None


class TestTextInbox:
    def test_invalid_channel_raises(self):
        client = CampaignClient("https://api.example.test", access_token="tok")
        with pytest.raises(ValueError, match="alerts.*updates"):
            TextInbox(client, "bogus")

    @patch("saucepan.campaigns.requests.request")
    def test_ack(self, mock_request):
        client = CampaignClient("https://api.example.test", access_token="tok")
        resp = Mock(status_code=200, content=b"{}")
        resp.json.return_value = {}
        mock_request.return_value = resp

        client.updates.ack("evt-1")
        assert "/api/v1/updates/evt-1/ack" in mock_request.call_args.args[1]
        assert mock_request.call_args.args[0] == "POST"

    def test_run_worker_stop_event_already_set(self):
        client = CampaignClient("https://api.example.test", access_token="tok")
        stop = threading.Event()
        stop.set()

        with patch.object(client.alerts, "poll") as mock_poll:
            client.alerts.run_worker(lambda e: None, stop_event=stop, poll_interval=0.01)

        mock_poll.assert_not_called()

    def test_run_worker_poll_exception_caught(self):
        client = CampaignClient("https://api.example.test", access_token="tok")
        stop = threading.Event()
        calls = {"n": 0}

        def flaky(*, since=None, campaign_id=None):
            calls["n"] += 1
            if calls["n"] == 1:
                raise RuntimeError("blip")
            stop.set()
            return []

        with patch.object(client.alerts, "poll", side_effect=flaky):
            client.alerts.run_worker(lambda e: None, stop_event=stop, poll_interval=0.01)

        assert calls["n"] >= 2

    def test_run_worker_ack_on_failure(self):
        client = CampaignClient("https://api.example.test", access_token="tok")
        stop = threading.Event()
        calls = {"n": 0}

        def fake_poll(*, since=None, campaign_id=None):
            calls["n"] += 1
            if calls["n"] == 1:
                return [{"id": "e1", "message": "x"}]
            stop.set()
            return []

        def boom(_e):
            raise RuntimeError("callback failed")

        with (
            patch.object(client.alerts, "poll", side_effect=fake_poll),
            patch.object(client.alerts, "ack") as mock_ack,
        ):
            client.alerts.run_worker(boom, stop_event=stop, poll_interval=0.01, ack_on_failure=True)

        mock_ack.assert_called_once_with("e1")

    def test_run_worker_no_ack_when_event_missing_id(self):
        client = CampaignClient("https://api.example.test", access_token="tok")
        stop = threading.Event()
        calls = {"n": 0}

        def fake_poll(*, since=None, campaign_id=None):
            calls["n"] += 1
            if calls["n"] == 1:
                return [{"message": "no id here"}]
            stop.set()
            return []

        seen = []

        with (
            patch.object(client.alerts, "poll", side_effect=fake_poll),
            patch.object(client.alerts, "ack") as mock_ack,
        ):
            client.alerts.run_worker(seen.append, stop_event=stop, poll_interval=0.01)

        assert seen == [{"message": "no id here"}]
        mock_ack.assert_not_called()

    def test_run_worker_without_stop_event_sleeps_between_polls(self):
        """No stop_event provided — the loop uses real time.sleep(); break out
        by making the patched sleep raise after the first iteration."""
        client = CampaignClient("https://api.example.test", access_token="tok")
        calls = {"n": 0}

        def fake_poll(*, since=None, campaign_id=None):
            calls["n"] += 1
            return []

        class _StopLoopError(Exception):
            pass

        with (
            patch.object(client.alerts, "poll", side_effect=fake_poll),
            patch("saucepan.campaigns.time.sleep", side_effect=_StopLoopError),
        ):
            with pytest.raises(_StopLoopError):
                client.alerts.run_worker(lambda e: None, poll_interval=0.01)

        assert calls["n"] == 1


class TestCampaignTaskBudgetEdgeCases:
    def test_pct_complete_zero_budget_returns_zero(self):
        t = CampaignTask(id="t1", name="x", status="pending", normalized_integration_budget_s=0.0)
        assert t.pct_complete == 0.0

    def test_is_budget_complete_zero_budget_uses_status(self):
        t = CampaignTask(id="t1", name="x", status="completed", normalized_integration_budget_s=0.0)
        assert t.is_budget_complete is True

        t2 = CampaignTask(id="t2", name="x", status="pending", normalized_integration_budget_s=0.0)
        assert t2.is_budget_complete is False

    def test_pct_complete_clamped_at_100(self):
        t = CampaignTask(
            id="t1",
            name="x",
            status="pending",
            normalized_integration_budget_s=10.0,
            normalized_integration_earned_s=25.0,
        )
        assert t.pct_complete == 100.0
        assert t.budget_remaining == 0.0  # clamped, never negative


class TestFromSessionAndRefreshAuth:
    def test_from_session_copies_tokens(self):
        session = ResearcherSession(access_token="jwt", refresh_token="rjwt")
        client = CampaignClient.from_session("https://api.example.test", session)
        assert client.access_token == "jwt"
        assert client.refresh_token == "rjwt"

    @patch("saucepan.campaigns.requests.post")
    def test_refresh_auth_updates_client_tokens(self, mock_post):
        client = CampaignClient(
            "https://api.example.test", access_token="stale", refresh_token="old-refresh"
        )
        resp = Mock(ok=True, status_code=200)
        resp.json.return_value = {"access_token": "fresh", "refresh_token": "new-refresh"}
        mock_post.return_value = resp

        session = client.refresh_auth()

        assert session.access_token == "fresh"
        assert client.access_token == "fresh"
        assert client.refresh_token == "new-refresh"


class TestRefreshResearcherSessionConnectionError:
    @patch(
        "saucepan.campaigns.requests.post",
        side_effect=__import__("requests").ConnectionError("down"),
    )
    def test_connection_error_raises_server_error(self, mock_post):
        with pytest.raises(ServerError, match="Connection failed"):
            refresh_researcher_session("https://api.example.test", "rtok")


class TestAccountUsageUntilParam:
    @patch("saucepan.campaigns.requests.request")
    def test_until_param_included(self, mock_request):
        client = CampaignClient("https://api.example.test", access_token="tok")
        resp = Mock(status_code=200, content=b"{}")
        resp.json.return_value = {}
        mock_request.return_value = resp

        client.account_usage(until="2026-06-01T00:00:00Z")
        assert mock_request.call_args.kwargs["params"]["until"] == "2026-06-01T00:00:00Z"


class TestCoverageMetricsFromDeliveries:
    def test_without_pack_calls_compute_coverage_metrics(self):
        client = CampaignClient("https://api.example.test", access_token="tok")
        out = client.coverage_metrics_from_deliveries([])
        assert out.status == "insufficient_data"

    def test_with_pack_calls_metrics_from_pack(self):
        client = CampaignClient("https://api.example.test", access_token="tok")
        pack = CampaignPack(name="x", coverage={"mode": "soft"})
        out = client.coverage_metrics_from_deliveries([], pack=pack)
        assert out.status == "insufficient_data"


class TestNormalizePack:
    def test_normalize_pack_passthrough(self):
        pack = CampaignPack(name="x", targets=[])
        assert _normalize_pack(pack) is pack

    def test_normalize_pack_from_dict(self):
        out = _normalize_pack({"name": "from-dict", "targets": []})
        assert isinstance(out, CampaignPack)
        assert out.name == "from-dict"

    def test_normalize_pack_from_path(self, tmp_path):
        import json

        p = tmp_path / "pack.json"
        p.write_text(json.dumps({"name": "from-path", "targets": []}))
        out = _normalize_pack(p)
        assert out.name == "from-path"

    def test_normalize_pack_from_str_path(self, tmp_path):
        import json

        p = tmp_path / "pack.json"
        p.write_text(json.dumps({"name": "from-str", "targets": []}))
        out = _normalize_pack(str(p))
        assert out.name == "from-str"


class TestSafeJsonAndOptFloat:
    def test_safe_json_returns_empty_dict_on_exception(self):
        resp = Mock()
        resp.json.side_effect = ValueError("not json")
        assert _safe_json(resp) == {}

    def test_safe_json_returns_parsed_body(self):
        resp = Mock()
        resp.json.return_value = {"error": "x"}
        assert _safe_json(resp) == {"error": "x"}

    def test_opt_float_none_input(self):
        assert _opt_float(None) is None

    def test_opt_float_valid_string(self):
        assert _opt_float("12.5") == 12.5

    def test_opt_float_invalid_string_returns_none(self):
        assert _opt_float("not-a-number") is None

    def test_opt_float_invalid_type_returns_none(self):
        assert _opt_float(object()) is None
