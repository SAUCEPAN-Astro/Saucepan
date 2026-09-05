"""Tests that FITS download paths enforce landing URL allowlist."""

from pathlib import Path
from unittest.mock import Mock

import pytest

from saucepan import exceptions
from saucepan._http import _HttpSession
from saucepan.campaign_inbox import _download_url

_R2_URL = "https://abc123.r2.cloudflarestorage.com/saucepan/frame.fits"


class TestHttpDownloadFitsAllowlist:
    def test_download_fits_rejects_disallowed_host(self, tmp_path):
        session = _HttpSession(api_key="k", base_url="https://api.example")
        with pytest.raises(exceptions.ValidationError, match="not on allowlist"):
            session.download_fits(42, "https://evil.example/frame.fits", str(tmp_path))

    def test_download_fits_allows_r2_host(self, tmp_path):
        session = _HttpSession(api_key="k", base_url="https://api.example")
        response = Mock(ok=True, status_code=200)
        response.iter_content.return_value = [b"FITS"]
        session._download_session.get = Mock(return_value=response)

        path = session.download_fits(42, _R2_URL, str(tmp_path))
        assert path.endswith("42.fits")
        session._download_session.get.assert_called_once_with(
            _R2_URL,
            stream=True,
            timeout=300,
            allow_redirects=False,
        )

    def test_download_fits_sanitizes_task_id(self, tmp_path):
        session = _HttpSession(api_key="k", base_url="https://api.example")
        response = Mock(ok=True, status_code=200, iter_content=lambda chunk_size: [b"FITS"])
        session._download_session.get = Mock(return_value=response)

        path = session.download_fits("../outside", _R2_URL, str(tmp_path))

        assert Path(path).parent == tmp_path
        assert ".." not in Path(path).name


class TestCampaignInboxDownloadAllowlist:
    def test_download_url_rejects_disallowed_host(self, tmp_path):
        with pytest.raises(ValueError, match="not on allowlist"):
            _download_url(
                "https://127.0.0.1/key.fits",
                tmp_path,
                "d1",
                "graded",
                30.0,
            )

    def test_download_url_allows_r2_host(self, tmp_path, monkeypatch):
        mock_get = Mock(
            return_value=Mock(
                status_code=200,
                ok=True,
                iter_content=lambda chunk_size: [b"FITS"],
            )
        )
        monkeypatch.setattr("saucepan.campaign_inbox.requests.get", mock_get)
        path = _download_url(_R2_URL, tmp_path, "d1", "graded", 30.0)
        assert path == str(tmp_path / "d1_graded.fits")
        mock_get.assert_called_once_with(
            _R2_URL,
            stream=True,
            timeout=30.0,
            allow_redirects=False,
        )

    def test_download_url_sanitizes_delivery_id(self, tmp_path, monkeypatch):
        mock_get = Mock(
            return_value=Mock(
                status_code=200,
                ok=True,
                iter_content=lambda chunk_size: [b"FITS"],
            )
        )
        monkeypatch.setattr("saucepan.campaign_inbox.requests.get", mock_get)

        path = _download_url(_R2_URL, tmp_path, "../outside", "graded", 30.0)

        assert Path(path).parent == tmp_path
        assert ".." not in Path(path).name
