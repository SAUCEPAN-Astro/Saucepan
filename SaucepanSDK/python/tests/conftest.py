"""
Test configuration and fixtures for Saucepan Python SDK.
"""

from unittest.mock import Mock

import pytest

import saucepan
import saucepan._http as _http
import saucepan.inbox as inbox
import saucepan.tasks as tasks
from saucepan import models


@pytest.fixture
def mock_http():
    """Mock HTTP session for testing."""
    mock = Mock(spec=_http._HttpSession)
    return mock


@pytest.fixture
def client(mock_http):
    """Client with mocked HTTP layer.

    Rebind tasks/inbox after swapping _session — those resources capture the
    session at Client.__init__ time and would otherwise hit the real network.
    """
    client = saucepan.Client("sp_live_test_key")
    client._session = mock_http
    client.tasks = tasks.TasksResource(mock_http)
    client.inbox = inbox.InboxResource(mock_http)
    return client


@pytest.fixture
def sample_task_spec():
    """Sample valid task specification."""
    return models.TaskSpec(
        name="M42",
        integration_time=300,
        min_power=0.7,
    )


@pytest.fixture
def sample_task():
    """Sample task response."""
    return models.Task(
        id="123",
        name="M42",
        status="pending",
        spec=models.TaskSpec(
            name="M42",
            integration_time=300,
            min_power=0.7,
        ),
    )


@pytest.fixture
def sample_delivery(sample_task_spec):
    """Sample delivery response."""
    return models.Delivery(
        notification_id=1,
        task_id="123",
        status="completed",
        failure_reason=None,
        _original_spec=sample_task_spec,
        _fits_url="https://example.com/fits/123.fits",
        _http=Mock(spec=_http._HttpSession),
    )


@pytest.fixture
def sample_quota():
    """Sample quota status."""
    return models.QuotaStatus(
        total=100,
        used=50,
    )
