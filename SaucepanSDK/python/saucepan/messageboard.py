"""Campaign signal board — the researcher's read/write access to a campaign's
opaque inter-telescope message stream over plain HTTP.

Piers coordinate on the retained MQTT board (`/board/campaign/{id}/{node}`);
the SDK holds no MQTT credential, so a researcher polls and posts
here instead. Auth is the same researcher access token the `CampaignClient`
already carries — there is no separate board token.

    board = campaigns.board(campaign_id)
    board.post("Z9A7", payload={"snr": 8.1})
    for note in board.poll():
        ...
    board.run(lambda n: print(n["author"], n["message"]))   # durable loop

The ``message`` is an arbitrary string. ``event_type`` and ``payload`` remain
optional compatibility metadata; receiving researcher code may ignore them
and interpret the string using its own rules.
"""

from __future__ import annotations

import logging
from collections.abc import Callable
from typing import Any

from saucepan._paths import path_segment

logger = logging.getLogger(__name__)


class CampaignBoard:
    """Bound to one CampaignClient + campaign_id."""

    def __init__(self, client: Any, campaign_id: str) -> None:
        if not campaign_id:
            raise ValueError("campaign_id is required")
        self._client = client
        self._campaign_id = path_segment(campaign_id, name="campaign_id")

    @property
    def campaign_id(self) -> str:
        return self._campaign_id

    def post(
        self,
        message: str,
        *,
        event_type: str = "note",
        payload: dict[str, Any] | None = None,
    ) -> dict[str, Any]:
        """Send one arbitrary string. Returns the stored message envelope."""
        body: dict[str, Any] = {"event_type": event_type, "message": message}
        if payload is not None:
            body["payload"] = payload
        data = self._client._request(
            "POST", f"/api/v1/campaigns/{self._campaign_id}/board", json=body
        )
        return dict(data.get("note") or {})

    def poll(
        self,
        *,
        since: str | None = None,
        after_id: str | None = None,
    ) -> list[dict[str, Any]]:
        """Notes in append order, optionally only those after `since`
        (an RFC3339 timestamp, with `after_id` to break ties)."""
        params: dict[str, str] = {}
        if since:
            params["since"] = since
        if after_id:
            params["after_id"] = after_id
        data = self._client._request(
            "GET", f"/api/v1/campaigns/{self._campaign_id}/board", params=params
        )
        return list(data.get("notes") or [])

    def run(
        self,
        on_note: Callable[[dict[str, Any]], None],
        *,
        poll_interval: float = 30.0,
        stop_event: Any | None = None,
        include_own: bool = False,
    ) -> None:
        """Durable poll loop (multi-month safe). Advances a `since` cursor from
        each note's `created_at`; a callback that raises is logged and the loop
        continues (the note is not re-queued — the cursor has moved). By
        default the researcher's own notes are skipped."""
        cursor: str | None = None
        last_id: str | None = None
        while True:
            if stop_event is not None and getattr(stop_event, "is_set", lambda: False)():
                return
            try:
                notes = self.poll(since=cursor, after_id=last_id if cursor else None)
            except Exception as exc:  # noqa: BLE001 - loop must survive transient errors
                logger.exception("board poll failed: %s", exc)
                notes = []

            for note in notes:
                created = note.get("created_at")
                if created:
                    cursor = str(created)
                    last_id = str(note.get("id") or "")
                if not include_own and note.get("author") == "researcher":
                    continue
                try:
                    on_note(note)
                except Exception:  # noqa: BLE001
                    logger.exception("on_note failed for board note %s", note.get("id"))

            if stop_event is not None:
                if stop_event.wait(poll_interval):
                    return
            else:
                import time

                time.sleep(poll_interval)
