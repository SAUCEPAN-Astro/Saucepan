import logging

import pytest

from saucepan.campaigns import DEFAULT_API_URL, CampaignClient
from saucepan.exceptions import ConfigurationError
from saucepan.quest import DEFAULT_QUEST_API_URL, QuestClient


def test_api_url_defaults() -> None:
    assert DEFAULT_API_URL == "http://127.0.0.1:8080"
    assert CampaignClient(access_token="token").base_url == DEFAULT_API_URL
    assert DEFAULT_QUEST_API_URL == "http://127.0.0.1:8080"
    assert QuestClient().base_url == DEFAULT_QUEST_API_URL


def test_cleartext_remote_url_is_rejected() -> None:
    with pytest.raises(ConfigurationError, match="HTTPS"):
        CampaignClient("http://example.test:8080", access_token="token")


def test_cleartext_localhost_does_not_warn(caplog) -> None:
    with caplog.at_level(logging.WARNING):
        CampaignClient("http://127.0.0.1:8080", access_token="token")
        CampaignClient("http://localhost:8080", access_token="token")
    assert "cleartext HTTP" not in caplog.text


def test_https_remote_url_does_not_warn(caplog) -> None:
    with caplog.at_level(logging.WARNING):
        CampaignClient("https://example.test", access_token="token")
    assert "cleartext HTTP" not in caplog.text
