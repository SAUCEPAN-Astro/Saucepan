"""
Tests for HTTP layer with mocked responses.
"""

import threading
from contextlib import contextmanager
from http.server import BaseHTTPRequestHandler, HTTPServer
from unittest.mock import Mock, patch

import pytest
import requests

from saucepan import exceptions, models
from saucepan._http import _HttpSession


def _mock_response(status_code: int, body: dict | None = None, headers: dict | None = None):
    """Build a requests-like response mock for _handle_response."""
    mock_response = Mock()
    mock_response.status_code = status_code
    mock_response.headers = headers or {}
    payload = body if body is not None else {}
    mock_response.json.return_value = payload
    # content truthy when body present so success path can parse JSON
    mock_response.content = b"{}" if payload else b""
    return mock_response


class TestHttpSession:
    """Tests for HTTP session handling."""

    @pytest.fixture
    def http_session(self):
        """Create HTTP session for testing."""
        return _HttpSession(
            api_key="sp_live_test",
            base_url="https://api.example.test",
        )

    def test_session_initialization(self, http_session):
        """Test HTTP session initialization."""
        assert http_session._base_url == "https://api.example.test"
        assert "X-API-Key" in http_session._session.headers
        assert http_session._session.headers["X-API-Key"] == "sp_live_test"

    def test_session_headers(self, http_session):
        """Test session has correct headers."""
        headers = http_session._session.headers

        assert headers["X-API-Key"] == "sp_live_test"
        assert headers["Content-Type"] == "application/json"
        assert headers["Accept"] == "application/json"

    def test_connection_pool_config(self):
        """Test connection pool configuration."""
        session = _HttpSession(
            api_key="sp_live_test",
            base_url="https://api.example.test",
            pool_connections=20,
            pool_maxsize=20,
        )

        assert session._session is not None


class TestErrorHandling:
    """Tests for error handling in HTTP layer."""

    @pytest.fixture
    def http_session(self):
        """Create HTTP session for testing."""
        return _HttpSession(
            api_key="sp_live_test",
            base_url="https://api.example.test",
        )

    def test_handle_401_error(self, http_session):
        """Test handling of 401 Unauthorized."""
        mock_response = _mock_response(401, {"error": "Invalid API key"})

        with pytest.raises(exceptions.AuthError) as exc:
            http_session._handle_response(mock_response)

        assert "Invalid API key" in str(exc.value)

    def test_handle_422_validation_error(self, http_session):
        """Test handling of 422 ValidationError."""
        mock_response = _mock_response(
            422,
            {"error": "Validation failed", "fields": {"name": "required"}},
        )

        with pytest.raises(exceptions.ValidationError) as exc:
            http_session._handle_response(mock_response)

        assert "name" in exc.value.fields

    def test_handle_429_rate_limit(self, http_session):
        """Test handling of 429 RateLimitError."""
        mock_response = _mock_response(
            429,
            {"error": "Rate limit exceeded"},
            headers={"Retry-After": "60"},
        )

        with pytest.raises(exceptions.RateLimitError) as exc:
            http_session._handle_response(mock_response)

        assert exc.value.retry_after == 60

    def test_handle_429_quota_exceeded(self, http_session):
        """Test handling of 429 QuotaError."""
        mock_response = _mock_response(
            429,
            {
                "error": "quota_exceeded",
                "quota_total": 100,
                "quota_used": 100,
            },
        )

        with pytest.raises(exceptions.QuotaError) as exc:
            http_session._handle_response(mock_response)

        assert exc.value.quota_total == 100
        assert exc.value.quota_used == 100

    def test_handle_404_not_found(self, http_session):
        """Test handling of 404 NotFoundError."""
        mock_response = _mock_response(404, {"error": "Task not found"})

        with pytest.raises(exceptions.NotFoundError) as exc:
            http_session._handle_response(mock_response)

        assert "Task not found" in str(exc.value)

    def test_handle_500_server_error(self, http_session):
        """Test handling of 500 ServerError."""
        mock_response = _mock_response(500, {"error": "Internal server error"})

        with pytest.raises(exceptions.ServerError) as exc:
            http_session._handle_response(mock_response)

        assert exc.value.status_code == 500


class TestRetryConfig:
    """
    # Retries must live in exactly one layer. The kept layer is urllib3's
    Retry, mounted on the session in __init__. These tests assert the
    constructor args (max_retries / retry_backoff_base) actually reach that
    Retry object instead of being silently ignored by a second hand-rolled loop.
    """

    def _retry_for(self, **kwargs):
        session = _HttpSession(
            api_key="sp_live_test",
            base_url="https://api.example.test",
            **kwargs,
        )
        adapter = session._session.get_adapter("https://api.example.test")
        return session, adapter.max_retries

    def test_constructor_args_feed_the_retry_object(self):
        session, retry = self._retry_for(max_retries=1, retry_backoff_base=2.0)

        # still exposed on the instance for callers/tests
        assert session._max_retries == 1
        assert session._retry_backoff_base == 2.0

        # ...and actually wired into the one retry layer (not ignored)
        assert retry.total == 1
        assert retry.connect == 1
        assert retry.read == 1
        assert retry.backoff_factor == 2.0
        assert set(retry.status_forcelist) == {500, 502, 503, 504}

    def test_defaults_match_module_constants(self):
        _, retry = self._retry_for()
        assert retry.total == 3
        assert retry.backoff_factor == 1.5

    def test_http_and_https_share_one_retry_config(self):
        session, https_retry = self._retry_for(max_retries=4)
        http_retry = session._session.get_adapter("http://api.example.test").max_retries
        assert http_retry.total == https_retry.total == 4

    def test_every_method_is_retryable(self):
        # allowed_methods=None => POST/PATCH retried too, matching the
        # method-blind hand-rolled loop that used to live in _request.
        _, retry = self._retry_for()
        assert retry.allowed_methods is None


class _AlwaysServiceUnavailable(BaseHTTPRequestHandler):
    """Answers every request 503 and counts how many it saw (server.hits)."""

    def _serve(self):
        self.server.hits += 1
        body = b'{"error": "down"}'
        self.send_response(503)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):  # noqa: N802 - stdlib BaseHTTPRequestHandler dispatch name
        self._serve()

    def do_POST(self):  # noqa: N802 - stdlib BaseHTTPRequestHandler dispatch name
        self._serve()

    def log_message(self, *args):  # silence default stderr logging
        pass


@contextmanager
def _counting_server():
    srv = HTTPServer(("127.0.0.1", 0), _AlwaysServiceUnavailable)
    srv.hits = 0
    thread = threading.Thread(target=srv.serve_forever, daemon=True)
    thread.start()
    try:
        yield srv
    finally:
        srv.shutdown()
        thread.join(timeout=5)


class TestRequestRetryBehaviour:
    """
    # One retry layer, honouring max_retries, and a persistent 5xx
    ends in a ServerError with an "after N attempts" message. These hit a real
    loopback server so the retrying (which now happens *below*
    requests.Session.request) is actually exercised.
    """

    def _session(self, port, **kwargs):
        return _HttpSession(
            api_key="sp_live_test",
            base_url=f"http://127.0.0.1:{port}",
            retry_backoff_base=0.0,  # no real sleeping between retries
            **kwargs,
        )

    def test_persistent_503_makes_exactly_max_retries_plus_one_calls(self):
        with _counting_server() as srv:
            session = self._session(srv.server_address[1], max_retries=2)
            with pytest.raises(exceptions.ServerError, match="after 3 attempts"):
                session._request("GET", "/tasks")
        assert srv.hits == 3  # 1 initial + 2 retries, not a nested retry loop

    def test_max_retries_one_limits_to_two_calls(self):
        with _counting_server() as srv:
            session = self._session(srv.server_address[1], max_retries=1)
            with pytest.raises(exceptions.ServerError, match="after 2 attempts"):
                session._request("GET", "/tasks")
        assert srv.hits == 2

    def test_zero_retries_makes_a_single_call(self):
        with _counting_server() as srv:
            session = self._session(srv.server_address[1], max_retries=0)
            with pytest.raises(exceptions.ServerError, match="after 1 attempts"):
                session._request("GET", "/tasks")
        assert srv.hits == 1

    def test_post_is_retried_too(self):
        with _counting_server() as srv:
            session = self._session(srv.server_address[1], max_retries=2)
            with pytest.raises(exceptions.ServerError, match="after 3 attempts"):
                session._request("POST", "/tasks", json={"name": "M42"})
        assert srv.hits == 3


class TestRequestErrorMapping:
    """_request's own handling, with the transport mocked exactly one call deep."""

    @pytest.fixture
    def http_session(self):
        return _HttpSession(api_key="sp_live_test", base_url="https://api.example.test")

    def test_connection_error_raises_server_error(self, http_session):
        with patch.object(
            http_session._session, "request", side_effect=requests.ConnectionError("no route")
        ):
            with pytest.raises(exceptions.ServerError, match="Connection failed"):
                http_session._request("GET", "/tasks")

    def test_timeout_raises_server_error(self, http_session):
        with patch.object(http_session._session, "request", side_effect=requests.Timeout("slow")):
            with pytest.raises(exceptions.ServerError, match="timed out"):
                http_session._request("GET", "/tasks")

    def test_retryable_status_becomes_server_error_after_n_attempts(self, http_session):
        # _session.request hands back the *already-retried* response; _request
        # must convert a still-5xx result into the "after N attempts" ServerError
        # and must not loop itself (regression guard for a doubled retry).
        resp = _mock_response(503, {"error": "down"})
        with patch.object(http_session._session, "request", return_value=resp) as mock_req:
            with pytest.raises(exceptions.ServerError, match="after 4 attempts"):
                http_session._request("GET", "/tasks")
        assert mock_req.call_count == 1

    def test_retryable_status_server_error_carries_status_code(self, http_session):
        resp = _mock_response(500, {"error": "boom"})
        with patch.object(http_session._session, "request", return_value=resp):
            with pytest.raises(exceptions.ServerError) as exc:
                http_session._request("GET", "/tasks")
        assert exc.value.status_code == 500

    def test_success_returns_parsed_body_in_one_call(self, http_session):
        resp = _mock_response(200, {"ok": True})
        with patch.object(http_session._session, "request", return_value=resp) as mock_req:
            assert http_session._request("GET", "/tasks") == {"ok": True}
        assert mock_req.call_count == 1

    def test_non_retryable_error_maps_without_retry(self, http_session):
        resp = _mock_response(404, {"error": "nope"})
        with patch.object(http_session._session, "request", return_value=resp) as mock_req:
            with pytest.raises(exceptions.NotFoundError):
                http_session._request("GET", "/tasks/1")
        assert mock_req.call_count == 1


class TestHandleResponseEdgeCases:
    @pytest.fixture
    def http_session(self):
        return _HttpSession(api_key="sp_live_test", base_url="https://api.example.test")

    def test_200_with_empty_content_returns_empty_dict(self, http_session):
        resp = _mock_response(200)
        resp.content = b""
        assert http_session._handle_response(resp) == {}

    def test_201_with_body_parses_json(self, http_session):
        resp = _mock_response(201, {"id": 1})
        assert http_session._handle_response(resp) == {"id": 1}

    def test_403_raises_auth_error(self, http_session):
        resp = _mock_response(403, {"error": "forbidden"})
        with pytest.raises(exceptions.AuthError, match="forbidden"):
            http_session._handle_response(resp)

    def test_malformed_json_body_falls_back_to_defaults(self, http_session):
        resp = _mock_response(401)
        resp.json.side_effect = ValueError("not json")
        with pytest.raises(exceptions.AuthError, match="Authentication failed"):
            http_session._handle_response(resp)

    def test_429_non_numeric_retry_after_yields_none(self, http_session):
        resp = _mock_response(429, {"error": "slow_down"}, headers={"Retry-After": "not-a-number"})
        with pytest.raises(exceptions.RateLimitError) as exc:
            http_session._handle_response(resp)
        assert exc.value.retry_after is None

    def test_429_missing_retry_after_defaults_zero(self, http_session):
        resp = _mock_response(429, {"error": "slow_down"})
        with pytest.raises(exceptions.RateLimitError) as exc:
            http_session._handle_response(resp)
        assert exc.value.retry_after == 0


class TestPublicMethods:
    """Tests for the thin public wrappers that go through _request."""

    @pytest.fixture
    def http_session(self):
        return _HttpSession(api_key="sp_live_test", base_url="https://api.example.test")

    def _task_body(self, **overrides):
        body = {
            "id": 7,
            "name": "M42",
            "status": "pending",
            "integration_time": 300,
            "min_power": 0.7,
        }
        body.update(overrides)
        return body

    def test_submit_task(self, http_session):
        spec = models.TaskSpec(name="M42", integration_time=300, min_power=0.7)
        # submit_task's response body is the task dict directly (no "task" wrapper).
        resp = _mock_response(201, self._task_body())
        with patch.object(http_session._session, "request", return_value=resp) as mock_req:
            task = http_session.submit_task(spec)

        assert task.id == "7"
        assert task.name == "M42"
        method, url = mock_req.call_args.args[:2]
        assert method == "POST"
        assert url.endswith("/tasks")

    def test_get_task(self, http_session):
        resp = _mock_response(200, self._task_body(id=42))
        with patch.object(http_session._session, "request", return_value=resp) as mock_req:
            task = http_session.get_task(42)

        assert task.id == "42"
        assert mock_req.call_args.args[1].endswith("/tasks/42")

    def test_get_task_uses_developer_task_id_fallback(self, http_session):
        body = self._task_body()
        del body["id"]
        body["developer_task_id"] = 99
        resp = _mock_response(200, body)
        with patch.object(http_session._session, "request", return_value=resp):
            task = http_session.get_task(99)

        assert task.id == "99"

    def test_list_tasks_dict_wrapped(self, http_session):
        resp = _mock_response(200, {"tasks": [self._task_body(id=1), self._task_body(id=2)]})
        with patch.object(http_session._session, "request", return_value=resp) as mock_req:
            tasks = http_session.list_tasks(status="pending", page=2, per_page=10)

        assert [t.id for t in tasks] == ["1", "2"]
        assert mock_req.call_args.kwargs["params"] == {
            "page": 2,
            "per_page": 10,
            "status": "pending",
        }

    def test_list_tasks_bare_list_response(self, http_session):
        resp = _mock_response(200, None)
        resp.json.return_value = [self._task_body(id=5)]
        resp.content = b"[...]"
        with patch.object(http_session._session, "request", return_value=resp):
            tasks = http_session.list_tasks()

        assert [t.id for t in tasks] == ["5"]

    def test_poll_inbox(self, http_session):
        delivery_body = {
            "notification_id": "10",
            "task_id": "7",
            "status": "completed",
            "original_spec": {"name": "M42", "integration_time": 300},
            "fits_url": "https://x.r2.cloudflarestorage.com/f.fits",
        }
        resp = _mock_response(200, {"deliveries": [delivery_body]})
        with patch.object(http_session._session, "request", return_value=resp) as mock_req:
            deliveries = http_session.poll_inbox()

        assert deliveries[0].notification_id == 10
        assert deliveries[0].task_id == "7"
        assert mock_req.call_args.args[1].endswith("/inbox")
        assert (
            "params" not in mock_req.call_args.kwargs
            or mock_req.call_args.kwargs.get("params") is None
        )

    def test_poll_inbox_all_passes_all_true(self, http_session):
        resp = _mock_response(200, {"deliveries": []})
        with patch.object(http_session._session, "request", return_value=resp) as mock_req:
            http_session.poll_inbox_all()

        assert mock_req.call_args.kwargs["params"] == {"all": "true"}

    def test_acknowledge_notification(self, http_session):
        resp = _mock_response(200, {})
        with patch.object(http_session._session, "request", return_value=resp) as mock_req:
            http_session.acknowledge_notification(123)

        method, url = mock_req.call_args.args[:2]
        assert method == "PATCH"
        assert url.endswith("/inbox/123")
        assert mock_req.call_args.kwargs["json"] == {"acknowledged": True}

    def test_get_download_url(self, http_session):
        resp = _mock_response(200, {"url": "https://x.r2.cloudflarestorage.com/f.fits"})
        with patch.object(http_session._session, "request", return_value=resp):
            url = http_session.get_download_url(7)

        assert url == "https://x.r2.cloudflarestorage.com/f.fits"

    def test_get_quota(self, http_session):
        resp = _mock_response(200, {"quota_total": 100, "quota_used": 40})
        with patch.object(http_session._session, "request", return_value=resp):
            quota = http_session.get_quota()

        assert quota.total == 100
        assert quota.used == 40


class TestDownloadFits:
    @pytest.fixture
    def http_session(self):
        return _HttpSession(api_key="sp_live_test", base_url="https://api.example.test")

    def test_download_fits_success(self, http_session, tmp_path):
        resp = Mock(ok=True, status_code=200)
        resp.iter_content.return_value = [b"FITSDATA"]
        with patch.object(http_session._download_session, "get", return_value=resp):
            path = http_session.download_fits(
                7, "https://bucket.r2.cloudflarestorage.com/f.fits", str(tmp_path)
            )

        assert path == str(tmp_path / "7.fits")
        with open(path, "rb") as f:
            assert f.read() == b"FITSDATA"

    def test_download_fits_rejected_landing_url_raises_validation_error(
        self, http_session, tmp_path
    ):
        with pytest.raises(exceptions.ValidationError):
            http_session.download_fits(7, "https://evil.example.com/f.fits", str(tmp_path))

    def test_download_fits_non_ok_response_raises_server_error(self, http_session, tmp_path):
        resp = Mock(ok=False, status_code=500)
        with patch.object(http_session._download_session, "get", return_value=resp):
            with pytest.raises(exceptions.ServerError, match="Download failed"):
                http_session.download_fits(
                    7, "https://bucket.r2.cloudflarestorage.com/f.fits", str(tmp_path)
                )
