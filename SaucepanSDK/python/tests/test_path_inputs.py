"""Tests for safe URL path construction and transport policy."""

from unittest.mock import Mock

import pytest

from saucepan import Client, exceptions
from saucepan._paths import path_segment
from saucepan.campaigns import CampaignClient
from saucepan.quest import QuestClient


def test_path_segment_rejects_path_and_query_injection():
    for value in ("../secret", "a/b", "a?next=evil", "a#fragment", ""):
        with pytest.raises(ValueError):
            path_segment(value)


def test_download_session_has_no_api_key():
    client = Client("sp_live_test")
    assert "X-API-Key" not in client._session._download_session.headers


def test_quest_rejects_remote_cleartext():
    with pytest.raises(exceptions.ConfigurationError, match="HTTPS"):
        QuestClient("http://remote.example")


def test_campaign_ids_are_validated_before_request():
    client = CampaignClient("http://127.0.0.1:8080", access_token="token")
    client._request = Mock()
    with pytest.raises(ValueError):
        client.get("../../other")
