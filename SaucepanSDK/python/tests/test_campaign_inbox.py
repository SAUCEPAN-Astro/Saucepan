"""Tests for campaign delivery inbox worker."""

import threading
from unittest.mock import Mock, patch

import pytest

from saucepan.campaign_inbox import CampaignDelivery, CampaignDeliveryInbox, _download_url
from saucepan.campaigns import CampaignClient


@pytest.fixture
def client():
    return CampaignClient("https://api.example.test", access_token="tok")


class TestCampaignDeliveryInbox:
    @patch("saucepan.campaigns.requests.request")
    def test_poll(self, mock_request, client):
        mock_resp = Mock()
        mock_resp.status_code = 200
        mock_resp.content = b'{"deliveries":[{"id":"d1","status":"completed","campaign_id":"c1"}]}'
        mock_resp.json.return_value = {
            "deliveries": [{"id": "d1", "status": "completed", "campaign_id": "c1"}]
        }
        mock_request.return_value = mock_resp

        inbox = CampaignDeliveryInbox(client)
        items = inbox.poll(campaign_id="c1")
        assert len(items) == 1
        assert items[0].id == "d1"

    @patch("saucepan.campaigns.requests.request")
    def test_run_worker_acks_on_success(self, mock_request, client):
        deliveries = [
            {"id": "d1", "status": "completed", "campaign_id": "c1", "created_at": "t1"},
            {"id": "d2", "status": "completed", "campaign_id": "c1", "created_at": "t2"},
        ]
        calls = {"n": 0}

        def fake_request(method, url, **kwargs):
            resp = Mock()
            resp.status_code = 200
            resp.content = b"{}"
            if method == "GET":
                idx = calls["n"]
                calls["n"] += 1
                if idx < len(deliveries):
                    resp.json.return_value = {"deliveries": [deliveries[idx]]}
                else:
                    resp.json.return_value = {"deliveries": []}
            else:
                resp.json.return_value = {"acknowledged": True}
            return resp

        mock_request.side_effect = fake_request

        inbox = CampaignDeliveryInbox(client)
        seen: list[str] = []
        stop = threading.Event()

        def on_delivery(d: CampaignDelivery) -> None:
            seen.append(d.id)

        def stop_after():
            time.sleep(0.2)
            stop.set()

        import time

        threading.Thread(target=stop_after, daemon=True).start()
        inbox.run_worker(on_delivery, poll_interval=0.05, stop_event=stop, auto_download=False)

        assert seen == ["d1", "d2"]
        ack_posts = [c for c in mock_request.call_args_list if c.args[0] == "POST"]
        assert len(ack_posts) == 2

    @patch("saucepan.campaign_inbox.requests.get")
    @patch("saucepan.campaigns.requests.request")
    def test_run_worker_auto_downloads_by_default(self, mock_request, mock_get, client, tmp_path):
        mock_get.return_value = Mock(
            status_code=200, ok=True, iter_content=lambda chunk_size: [b"FITS"]
        )
        deliveries = [
            {
                "id": "d1",
                "status": "completed",
                "campaign_id": "c1",
                "created_at": "t1",
                "graded_download_url": "https://abc123.r2.cloudflarestorage.com/graded.fits",
            },
        ]
        calls = {"n": 0}

        def fake_request(method, url, **kwargs):
            resp = Mock()
            resp.status_code = 200
            resp.content = b"{}"
            if method == "GET":
                idx = calls["n"]
                calls["n"] += 1
                resp.json.return_value = {"deliveries": deliveries if idx == 0 else []}
            else:
                resp.json.return_value = {"acknowledged": True}
            return resp

        mock_request.side_effect = fake_request
        inbox = CampaignDeliveryInbox(client)
        seen: list[str] = []
        stop = threading.Event()

        def on_delivery(d: CampaignDelivery) -> None:
            assert d.local_graded_path
            seen.append(d.id)

        def stop_after():
            import time

            time.sleep(0.2)
            stop.set()

        threading.Thread(target=stop_after, daemon=True).start()
        inbox.run_worker(
            on_delivery,
            poll_interval=0.05,
            stop_event=stop,
            data_dir=tmp_path,
        )
        assert seen == ["d1"]
        mock_get.assert_called()
        assert (tmp_path / "d1_graded.fits").exists()

    @patch("saucepan.campaigns.requests.request")
    def test_run_worker_no_ack_on_failure(self, mock_request, client):
        resp_get = Mock()
        resp_get.status_code = 200
        resp_get.content = b'{"deliveries":[{"id":"d1","status":"completed","campaign_id":"c1"}]}'
        resp_get.json.return_value = {
            "deliveries": [{"id": "d1", "status": "completed", "campaign_id": "c1"}]
        }
        mock_request.return_value = resp_get

        inbox = CampaignDeliveryInbox(client)
        stop = threading.Event()

        def boom(_d):
            raise RuntimeError("handler failed")

        def stop_after():
            import time

            time.sleep(0.15)
            stop.set()

        threading.Thread(target=stop_after, daemon=True).start()
        inbox.run_worker(
            boom,
            poll_interval=0.05,
            stop_event=stop,
            ack_on_failure=False,
            auto_download=False,
        )

        ack_posts = [c for c in mock_request.call_args_list if c.args and c.args[0] == "POST"]
        assert len(ack_posts) == 0


class TestTextInboxWorker:
    @patch("saucepan.campaigns.requests.request")
    def test_alerts_run_worker_acks(self, mock_request, client):
        events = [
            {"id": "a1", "message": "rejected", "created_at": "t1"},
            {"id": "a2", "message": "rejected", "created_at": "t2"},
        ]
        calls = {"n": 0}

        def fake_request(method, url, **kwargs):
            resp = Mock()
            resp.status_code = 200
            resp.content = b"{}"
            if method == "GET":
                idx = calls["n"]
                calls["n"] += 1
                if idx < len(events):
                    resp.json.return_value = {"alerts": [events[idx]]}
                else:
                    resp.json.return_value = {"alerts": []}
            else:
                resp.json.return_value = {"acknowledged": True}
            return resp

        mock_request.side_effect = fake_request
        seen: list[str] = []
        stop = threading.Event()

        def on_event(ev):
            seen.append(ev["id"])

        def stop_after():
            import time

            time.sleep(0.2)
            stop.set()

        threading.Thread(target=stop_after, daemon=True).start()
        client.alerts.run_worker(on_event, poll_interval=0.05, stop_event=stop)
        assert seen == ["a1", "a2"]


class TestCampaignDeliveryDownload:
    def _delivery(self, **overrides):
        data = {
            "id": "d1",
            "status": "completed",
            "campaign_id": "c1",
            "raw_download_url": "https://abc.r2.cloudflarestorage.com/raw.fits",
            "graded_download_url": "https://abc.r2.cloudflarestorage.com/graded.fits",
        }
        data.update(overrides)
        return CampaignDelivery.from_dict(data)

    def test_delivery_repr_hides_urls_and_local_paths(self):
        delivery = self._delivery()
        delivery.local_graded_path = "/home/alice/graded.fits"

        rendered = repr(delivery)

        assert "r2.cloudflarestorage.com" not in rendered
        assert "/home/alice" not in rendered

    @patch("saucepan.campaign_inbox.requests.get")
    def test_download_raw(self, mock_get, tmp_path):
        mock_get.return_value = Mock(
            status_code=200, ok=True, iter_content=lambda chunk_size: [b"RAW"]
        )
        delivery = self._delivery()

        path = delivery.download_raw(tmp_path)

        assert path == str(tmp_path / "d1_raw.fits")
        assert delivery.local_raw_path == path
        assert delivery.local_path == path  # no graded path set, falls back to raw

    @patch("saucepan.campaign_inbox.requests.get")
    def test_download_kind_raw(self, mock_get, tmp_path):
        mock_get.return_value = Mock(
            status_code=200, ok=True, iter_content=lambda chunk_size: [b"RAW"]
        )
        delivery = self._delivery()

        result = delivery.download(tmp_path, kind="raw")

        assert result == str(tmp_path / "d1_raw.fits")
        assert delivery.local_graded_path is None

    @patch("saucepan.campaign_inbox.requests.get")
    def test_download_kind_both(self, mock_get, tmp_path):
        mock_get.return_value = Mock(
            status_code=200, ok=True, iter_content=lambda chunk_size: [b"X"]
        )
        delivery = self._delivery()

        graded, raw = delivery.download(tmp_path, kind="both")

        assert graded == str(tmp_path / "d1_graded.fits")
        assert raw == str(tmp_path / "d1_raw.fits")
        assert mock_get.call_count == 2

    @patch("saucepan.campaign_inbox.requests.get")
    def test_download_default_kind_is_graded(self, mock_get, tmp_path):
        mock_get.return_value = Mock(
            status_code=200, ok=True, iter_content=lambda chunk_size: [b"G"]
        )
        delivery = self._delivery()

        result = delivery.download(tmp_path)

        assert result == str(tmp_path / "d1_graded.fits")
        assert delivery.local_raw_path is None

    def test_local_path_prefers_graded_over_raw(self):
        delivery = self._delivery()
        delivery.local_graded_path = "/tmp/graded.fits"
        delivery.local_raw_path = "/tmp/raw.fits"
        assert delivery.local_path == "/tmp/graded.fits"

    def test_local_path_none_when_nothing_downloaded(self):
        delivery = self._delivery()
        assert delivery.local_path is None


class TestDownloadUrlErrors:
    def test_no_url_raises_value_error(self, tmp_path):
        with pytest.raises(ValueError, match="No download URL"):
            _download_url(None, tmp_path, "d1", "graded", 30.0)

    def test_empty_url_raises_value_error(self, tmp_path):
        with pytest.raises(ValueError, match="No download URL"):
            _download_url("", tmp_path, "d1", "graded", 30.0)

    def test_rejected_landing_url_raises_value_error(self, tmp_path):
        with pytest.raises(ValueError):
            _download_url("https://evil.example.com/f.fits", tmp_path, "d1", "graded", 30.0)

    @patch("saucepan.campaign_inbox.requests.get")
    def test_hot_url_gone_404_raises_runtime_error(self, mock_get, tmp_path):
        mock_get.return_value = Mock(status_code=404, ok=False)
        with pytest.raises(RuntimeError, match="Hot download unavailable"):
            _download_url(
                "https://abc.r2.cloudflarestorage.com/f.fits", tmp_path, "d1", "graded", 30.0
            )

    @patch("saucepan.campaign_inbox.requests.get")
    def test_hot_url_gone_410_raises_runtime_error(self, mock_get, tmp_path):
        mock_get.return_value = Mock(status_code=410, ok=False)
        with pytest.raises(RuntimeError, match="Hot download unavailable"):
            _download_url(
                "https://abc.r2.cloudflarestorage.com/f.fits", tmp_path, "d1", "graded", 30.0
            )

    @patch("saucepan.campaign_inbox.requests.get")
    def test_hot_url_forbidden_403_raises_runtime_error(self, mock_get, tmp_path):
        mock_get.return_value = Mock(status_code=403, ok=False)
        with pytest.raises(RuntimeError, match="Hot download unavailable"):
            _download_url(
                "https://abc.r2.cloudflarestorage.com/f.fits", tmp_path, "d1", "graded", 30.0
            )

    @patch("saucepan.campaign_inbox.requests.get")
    def test_generic_download_failure_raises_runtime_error(self, mock_get, tmp_path):
        mock_get.return_value = Mock(status_code=500, ok=False)
        with pytest.raises(RuntimeError, match="Download failed: HTTP 500"):
            _download_url(
                "https://abc.r2.cloudflarestorage.com/f.fits", tmp_path, "d1", "graded", 30.0
            )


class TestRunWorkerEdgeCases:
    def test_stop_event_already_set_returns_immediately(self, client):
        inbox = CampaignDeliveryInbox(client)
        stop = threading.Event()
        stop.set()

        with patch.object(inbox, "poll") as mock_poll:
            inbox.run_worker(lambda d: None, stop_event=stop, poll_interval=0.01)

        mock_poll.assert_not_called()

    def test_poll_exception_is_caught_and_logged(self, client):
        inbox = CampaignDeliveryInbox(client)
        stop = threading.Event()
        calls = {"n": 0}

        def flaky_poll(*, since=None, campaign_id=None):
            calls["n"] += 1
            if calls["n"] == 1:
                raise RuntimeError("network blip")
            stop.set()
            return []

        with patch.object(inbox, "poll", side_effect=flaky_poll):
            # Should not raise — exception is caught inside run_worker.
            inbox.run_worker(lambda d: None, stop_event=stop, poll_interval=0.01)

        assert calls["n"] >= 2

    def test_ack_on_failure_true_acks_despite_callback_exception(self, client):
        inbox = CampaignDeliveryInbox(client)
        stop = threading.Event()
        delivery = CampaignDelivery.from_dict(
            {"id": "d1", "status": "completed", "campaign_id": "c1", "created_at": "t1"}
        )
        calls = {"n": 0}

        def fake_poll(*, since=None, campaign_id=None):
            calls["n"] += 1
            if calls["n"] == 1:
                return [delivery]
            stop.set()
            return []

        def boom(_d):
            raise RuntimeError("callback failed")

        with (
            patch.object(inbox, "poll", side_effect=fake_poll),
            patch.object(inbox, "acknowledge") as mock_ack,
        ):
            inbox.run_worker(
                boom,
                stop_event=stop,
                poll_interval=0.01,
                ack_on_failure=True,
                auto_download=False,
            )

        mock_ack.assert_called_once_with("d1")

    def test_run_worker_without_stop_event_sleeps_between_polls(self, client):
        """No stop_event provided — the loop uses real time.sleep(); break out
        by making the patched sleep raise after the first iteration."""
        inbox = CampaignDeliveryInbox(client)
        calls = {"n": 0}

        def fake_poll(*, since=None, campaign_id=None):
            calls["n"] += 1
            return []

        class _StopLoopError(Exception):
            pass

        with (
            patch.object(inbox, "poll", side_effect=fake_poll),
            patch("saucepan.campaign_inbox.time.sleep", side_effect=_StopLoopError),
        ):
            with pytest.raises(_StopLoopError):
                inbox.run_worker(lambda d: None, poll_interval=0.01, auto_download=False)

        assert calls["n"] == 1
