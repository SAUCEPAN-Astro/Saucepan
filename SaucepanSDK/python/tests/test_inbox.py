"""Tests for InboxResource get/ack semantics (poll, poll_all, drain)."""

from unittest.mock import Mock

import pytest

from saucepan.inbox import InboxResource


@pytest.fixture
def mock_http():
    return Mock()


@pytest.fixture
def resource(mock_http):
    return InboxResource(mock_http)


class TestPoll:
    def test_poll_returns_unacked_deliveries(self, resource, mock_http):
        deliveries = [Mock(name="d1"), Mock(name="d2")]
        mock_http.poll_inbox.return_value = deliveries

        result = resource.poll()

        assert result == deliveries
        mock_http.poll_inbox.assert_called_once_with()

    def test_poll_empty_inbox(self, resource, mock_http):
        mock_http.poll_inbox.return_value = []

        result = resource.poll()

        assert result == []

    def test_poll_all_includes_acked(self, resource, mock_http):
        deliveries = [Mock(name="d1"), Mock(name="d2"), Mock(name="d3")]
        mock_http.poll_inbox_all.return_value = deliveries

        result = resource.poll_all()

        assert result == deliveries
        mock_http.poll_inbox_all.assert_called_once_with()

    def test_poll_all_empty(self, resource, mock_http):
        mock_http.poll_inbox_all.return_value = []

        assert resource.poll_all() == []


class TestDrain:
    def test_drain_empty_inbox_returns_empty_list(self, resource, mock_http):
        mock_http.poll_inbox.return_value = []

        result = resource.drain("/tmp/data")

        assert result == []

    def test_drain_downloads_completed_and_acks_by_default(self, resource, mock_http, tmp_path):
        delivery = Mock()
        delivery.status = "completed"
        delivery.download.return_value = str(tmp_path / "1.fits")
        mock_http.poll_inbox.return_value = [delivery]

        result = resource.drain(str(tmp_path))

        delivery.download.assert_called_once_with(str(tmp_path))
        assert delivery.local_path == str(tmp_path / "1.fits")
        delivery.acknowledge.assert_called_once()
        assert result == [delivery]

    def test_drain_skips_download_for_non_completed_status(self, resource, mock_http, tmp_path):
        delivery = Mock()
        delivery.status = "failed"
        mock_http.poll_inbox.return_value = [delivery]

        result = resource.drain(str(tmp_path))

        delivery.download.assert_not_called()
        # still acked by default even though it failed
        delivery.acknowledge.assert_called_once()
        assert result == [delivery]

    def test_drain_acknowledge_false_does_not_ack(self, resource, mock_http, tmp_path):
        delivery = Mock()
        delivery.status = "completed"
        delivery.download.return_value = str(tmp_path / "1.fits")
        mock_http.poll_inbox.return_value = [delivery]

        result = resource.drain(str(tmp_path), acknowledge=False)

        delivery.download.assert_called_once()
        delivery.acknowledge.assert_not_called()
        assert result == [delivery]

    def test_drain_multiple_deliveries_mixed_status(self, resource, mock_http, tmp_path):
        completed = Mock()
        completed.status = "completed"
        completed.download.return_value = str(tmp_path / "1.fits")
        failed = Mock()
        failed.status = "failed"
        mock_http.poll_inbox.return_value = [completed, failed]

        result = resource.drain(str(tmp_path))

        completed.download.assert_called_once()
        failed.download.assert_not_called()
        completed.acknowledge.assert_called_once()
        failed.acknowledge.assert_called_once()
        assert result == [completed, failed]

    def test_drain_already_acked_items_not_returned_by_poll(self, resource, mock_http, tmp_path):
        # poll() itself is responsible for excluding already-acked items;
        # drain() just trusts whatever poll() returns.
        mock_http.poll_inbox.return_value = []

        result = resource.drain(str(tmp_path))

        assert result == []
        mock_http.poll_inbox.assert_called_once_with()
