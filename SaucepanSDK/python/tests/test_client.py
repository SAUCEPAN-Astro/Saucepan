"""
Tests for Saucepan client and tasksresource.
"""

import pytest

from saucepan import Client, exceptions, models


class TestClient:
    """Tests for Client class."""

    def test_client_initialization(self):
        """Test client initialization with valid API key."""
        client = Client("sp_live_test_key")

        assert client._session is not None
        assert client.tasks is not None
        assert client.inbox is not None

    def test_client_invalid_api_key_empty(self):
        """Test client rejects empty API key."""
        with pytest.raises(ValueError) as exc:
            Client("")

        assert "Invalid API key format" in str(exc.value)

    def test_client_invalid_api_key_prefix(self):
        """Test client rejects invalid API key prefix."""
        with pytest.raises(ValueError) as exc:
            Client("invalid_key")

        assert "must start with 'sp_live_'" in str(exc.value)

    def test_client_custom_base_url(self):
        """Test client with custom base URL."""
        client = Client(api_key="sp_live_test", base_url="https://custom.api.com")

        assert client._session._base_url == "https://custom.api.com"

    def test_client_rejects_cleartext_remote_base_url(self):
        """API-key requests must not use cleartext transport remotely."""
        with pytest.raises(exceptions.ConfigurationError, match="HTTPS"):
            Client(api_key="sp_live_test", base_url="http://remote.example")

    def test_client_connection_pooling_config(self):
        """Test client with connection pooling configuration."""
        client = Client(
            api_key="sp_live_test",
            timeout=60.0,
            pool_connections=20,
            pool_maxsize=20,
            max_retries=5,
            retry_backoff_base=2.0,
        )

        assert client._session._timeout == 60.0
        assert client._session._max_retries == 5
        assert client._session._retry_backoff_base == 2.0


class TestTasksResource:
    """Tests for TasksResource."""

    def test_submit_task_success(self, client, sample_task):
        """Test successful task submission."""
        client._session.submit_task.return_value = sample_task

        task = client.tasks.submit(
            name="M42",
            integration_time=300,
            min_power=0.7,
        )

        assert task.id == "123"
        assert task.name == "M42"
        assert task.status == "pending"
        client._session.submit_task.assert_called_once()

    def test_submit_task_with_optional_fields(self, client, sample_task):
        """Test task submission with optional fields."""
        client._session.submit_task.return_value = sample_task

        client.tasks.submit(
            name="M42",
            integration_time=300,
            min_power=0.7,
            filters=["R", "G", "B"],
            max_psf_fwhm=2.5,
            priority=20,
        )

        client._session.submit_task.assert_called_once()
        # Verify spec was validated

    def test_submit_task_validation_error(self, client):
        """Test task submission with invalid data."""
        with pytest.raises(exceptions.ValidationError):
            client.tasks.submit(
                name="",  # Invalid: empty name
                integration_time=300,
                min_power=0.7,
            )

    def test_get_task(self, client, sample_task):
        """Test getting task by ID."""
        client._session.get_task.return_value = sample_task

        task = client.tasks.get(task_id=123)

        assert task.id == "123"
        client._session.get_task.assert_called_once_with(123)

    def test_list_tasks(self, client):
        """Test listing tasks."""
        tasks = [
            models.Task(
                id="1",
                name="M42",
                status="completed",
                spec=models.TaskSpec(name="M42", integration_time=300, min_power=0.7),
            ),
            models.Task(
                id="2",
                name="M31",
                status="pending",
                spec=models.TaskSpec(name="M31", integration_time=600, min_power=0.8),
            ),
        ]
        client._session.list_tasks.return_value = tasks

        result = client.tasks.list(status="pending", page=1, per_page=50)

        assert len(result) == 2
        client._session.list_tasks.assert_called_once_with(
            status="pending",
            page=1,
            per_page=50,
        )

    def test_list_tasks_per_page_max(self, client):
        """Test list tasks caps per_page at 100."""
        client._session.list_tasks.return_value = []

        client.tasks.list(status="completed", page=1, per_page=200)

        # Should clamp per_page to 100
        client._session.list_tasks.assert_called_once_with(
            status="completed",
            page=1,
            per_page=100,
        )

    def test_wait_for_completion_success(self, client):
        """Test waiting for task completion."""
        completed_task = models.Task(
            id="123",
            name="M42",
            status="completed",
            spec=models.TaskSpec(name="M42", integration_time=300, min_power=0.7),
        )
        client._session.get_task.return_value = completed_task

        task = client.tasks.wait_for_completion(
            task_id=123,
            poll_interval=1.0,
            timeout=10.0,
        )

        assert task.status == "completed"
        assert task.is_complete is True

    def test_wait_for_completion_timeout(self, client):
        """Test wait for completion times out."""
        pending_task = models.Task(
            id="123",
            name="M42",
            status="pending",
            spec=models.TaskSpec(name="M42", integration_time=300, min_power=0.7),
        )
        client._session.get_task.return_value = pending_task

        with pytest.raises(TimeoutError):
            client.tasks.wait_for_completion(
                task_id=123,
                poll_interval=0.1,
                timeout=0.5,
            )


class TestInboxResource:
    """Tests for InboxResource."""

    def test_poll_inbox(self, client, sample_delivery):
        """Test polling inbox."""
        sample_delivery._http = client._session
        client._session.poll_inbox.return_value = [sample_delivery]

        deliveries = client.inbox.poll()

        assert len(deliveries) == 1
        assert deliveries[0].task_id == "123"
        client._session.poll_inbox.assert_called_once()

    def test_poll_inbox_all(self, client, sample_delivery):
        """Test polling inbox for all deliveries."""
        sample_delivery._http = client._session
        client._session.poll_inbox_all.return_value = [sample_delivery]

        deliveries = client.inbox.poll_all()

        assert len(deliveries) == 1
        client._session.poll_inbox_all.assert_called_once()


class TestQuota:
    """Tests for quota management."""

    def test_get_quota(self, client, sample_quota):
        """Test getting quota status."""
        client._session.get_quota.return_value = sample_quota

        quota = client.quota()

        assert quota.total == 100
        assert quota.used == 50
        assert quota.remaining == 50
        client._session.get_quota.assert_called_once()


class TestExceptions:
    """Tests for exception classes."""

    def test_open_astro_error(self):
        """Test base SaucepanError."""
        err = exceptions.SaucepanError("Something went wrong")
        assert str(err) == "Something went wrong"

    def test_validation_error(self):
        """Test ValidationError."""
        err = exceptions.ValidationError(
            "Invalid task spec", fields={"name": "required", "integration_time": "must be > 0"}
        )

        assert err.message == "Invalid task spec"
        assert "name" in err.fields
        assert "integration_time" in err.fields

    def test_rate_limit_error(self):
        """Test RateLimitError."""
        err = exceptions.RateLimitError("Rate limit exceeded", retry_after=60)

        assert err.message == "Rate limit exceeded"
        assert err.retry_after == 60

    def test_quota_error(self):
        """Test QuotaError."""
        err = exceptions.QuotaError("Quota exhausted", quota_total=100, quota_used=100)

        assert err.message == "Quota exhausted"
        assert err.quota_total == 100
        assert err.quota_used == 100

    def test_server_error(self):
        """Test ServerError."""
        err = exceptions.ServerError("Internal server error", status_code=500)

        assert err.message == "Internal server error"
        assert err.status_code == 500
