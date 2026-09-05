"""Tests for the FITS landing URL allowlist."""

import logging

import pytest

from saucepan.exceptions import ConfigurationError
from saucepan.landing_urls import LandingURLRejected, validate_landing_url

_R2_URL = "https://deadbeef.r2.cloudflarestorage.com/saucepan/frame.fits"
# RFC 5737 TEST-NET-3 documentation address — never a real host.
_TASK_HOST = "203.0.113.10"


class TestValidateLandingURL:
    def test_allows_r2_presigned_host(self):
        assert validate_landing_url(_R2_URL) == _R2_URL

    def test_allows_r2_public_endpoint_host(self, monkeypatch):
        monkeypatch.setenv("R2_PUBLIC_ENDPOINT", "https://cdn.saucepan.example")
        url = "https://cdn.saucepan.example/bucket/key.fits"
        assert validate_landing_url(url) == url

    def test_allows_env_allowlist_host(self, monkeypatch):
        monkeypatch.setenv("SAUCEPAN_LANDING_HOST_ALLOWLIST", "landing.staging.example")
        url = "https://landing.staging.example/key.fits"
        assert validate_landing_url(url) == url

    def test_blocks_task_host_from_task_url(self, monkeypatch):
        monkeypatch.setenv("SAUCEPAN_TASK_URL", f"https://{_TASK_HOST}:8080")
        with pytest.raises(LandingURLRejected, match="task server"):
            validate_landing_url(f"https://{_TASK_HOST}:19000/saucepan/key")

    def test_task_url_without_host_is_configuration_error(self, monkeypatch):
        monkeypatch.setenv("SAUCEPAN_TASK_URL", "https:///only-a-path")
        with pytest.raises(ConfigurationError, match="SAUCEPAN_TASK_URL") as exc:
            validate_landing_url(_R2_URL)
        assert "only-a-path" not in str(exc.value)

    def test_invalid_url_error_does_not_echo_query_secret(self):
        with pytest.raises(LandingURLRejected) as exc:
            validate_landing_url("not-a-url?token=secret-value")
        assert "secret-value" not in str(exc.value)

    def test_no_task_url_configured_still_fails_closed_on_unknown_host(self):
        # Nothing baked in: an unknown host is rejected by the positive allowlist.
        with pytest.raises(LandingURLRejected, match="not on allowlist"):
            validate_landing_url(f"https://{_TASK_HOST}/key")

    def test_blocks_loopback(self):
        with pytest.raises(LandingURLRejected, match="not on allowlist"):
            validate_landing_url("https://127.0.0.1:9000/bucket/key")

    def test_blocks_metadata_ip(self):
        with pytest.raises(LandingURLRejected, match="metadata"):
            validate_landing_url("https://169.254.169.254/latest/meta-data")

    def test_blocks_arbitrary_external_host(self):
        with pytest.raises(LandingURLRejected, match="not on allowlist"):
            validate_landing_url("https://example.com/evil.fits")

    def test_blocks_http_without_unsafe(self):
        with pytest.raises(LandingURLRejected, match="https only"):
            validate_landing_url("http://deadbeef.r2.cloudflarestorage.com/key")

    def test_unsafe_allows_localhost_http(self, monkeypatch, caplog):
        monkeypatch.setenv("SAUCEPAN_LANDING_ALLOW_UNSAFE", "1")
        url = "http://127.0.0.1:9000/saucepan/key.fits"
        with caplog.at_level(logging.WARNING):
            assert validate_landing_url(url) == url
        assert "SAUCEPAN_LANDING_ALLOW_UNSAFE=1" in caplog.text

    def test_unsafe_allows_private_lan_http(self, monkeypatch, caplog):
        monkeypatch.setenv("SAUCEPAN_LANDING_ALLOW_UNSAFE", "1")
        url = "http://192.168.1.50:9000/saucepan/key.fits"
        with caplog.at_level(logging.WARNING):
            assert validate_landing_url(url) == url
        assert "private/link-local" in caplog.text

    def test_unsafe_still_blocks_metadata(self, monkeypatch):
        monkeypatch.setenv("SAUCEPAN_LANDING_ALLOW_UNSAFE", "1")
        with pytest.raises(LandingURLRejected, match="metadata"):
            validate_landing_url("http://169.254.169.254/latest/meta-data")

    def test_unsafe_still_blocks_task_server(self, monkeypatch):
        monkeypatch.setenv("SAUCEPAN_LANDING_ALLOW_UNSAFE", "1")
        monkeypatch.setenv("SAUCEPAN_TASK_URL", f"http://{_TASK_HOST}:8080")
        with pytest.raises(LandingURLRejected, match="task server"):
            validate_landing_url(f"http://{_TASK_HOST}/key")
