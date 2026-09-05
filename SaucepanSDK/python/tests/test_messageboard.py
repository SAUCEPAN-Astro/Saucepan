"""CampaignBoard — the researcher HTTP side of the campaign messageboard."""

from __future__ import annotations

import threading
from typing import Any

import pytest

from saucepan.messageboard import CampaignBoard


class FakeClient:
    """Records _request calls and returns queued responses."""

    def __init__(self, responses: list[dict[str, Any]] | None = None) -> None:
        self.calls: list[tuple[str, str, dict[str, Any]]] = []
        self._responses = list(responses or [])

    def _request(self, method: str, path: str, *, json=None, params=None):
        self.calls.append((method, path, {"json": json, "params": params}))
        return self._responses.pop(0) if self._responses else {}


def test_board_requires_campaign_id():
    with pytest.raises(ValueError):
        CampaignBoard(FakeClient(), "")


def test_post_shapes_the_request_and_returns_note():
    client = FakeClient([{"note": {"id": "n1", "author": "researcher", "message": "hi"}}])
    board = CampaignBoard(client, "camp-1")

    note = board.post("hi", event_type="note", payload={"snr": 9})

    method, path, kw = client.calls[0]
    assert method == "POST"
    assert path == "/api/v1/campaigns/camp-1/board"
    assert kw["json"] == {"event_type": "note", "message": "hi", "payload": {"snr": 9}}
    assert note["id"] == "n1"


def test_poll_passes_since_and_after_id():
    client = FakeClient([{"notes": [{"id": "n1"}, {"id": "n2"}]}])
    board = CampaignBoard(client, "camp-1")

    notes = board.poll(since="2026-09-02T00:00:00Z", after_id="n0")

    _, path, kw = client.calls[0]
    assert path == "/api/v1/campaigns/camp-1/board"
    assert kw["params"] == {"since": "2026-09-02T00:00:00Z", "after_id": "n0"}
    assert [n["id"] for n in notes] == ["n1", "n2"]


def test_run_skips_own_notes_and_advances_cursor():
    pages = [
        {
            "notes": [
                {"id": "a", "author": "pier_1", "message": "found", "created_at": "t1"},
                {"id": "b", "author": "researcher", "message": "ack", "created_at": "t2"},
            ]
        },
        {"notes": []},
    ]
    stop = threading.Event()

    class StoppingClient(FakeClient):
        def _request(self, method, path, *, json=None, params=None):
            out = super()._request(method, path, json=json, params=params)
            if len(self.calls) >= 2:  # stop right after the second poll
                stop.set()
            return out

    client = StoppingClient(pages)
    board = CampaignBoard(client, "camp-1")
    seen: list[str] = []

    board.run(lambda n: seen.append(n["id"]), poll_interval=0, stop_event=stop)

    assert seen == ["a"]  # researcher's own "b" skipped
    # the second poll carried the cursor from the last note seen on page 1
    assert client.calls[1][2]["params"].get("since") == "t2"
