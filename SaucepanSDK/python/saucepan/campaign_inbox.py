"""Campaign FITS delivery inbox (poll + durable worker).

Hot presigned URLs may expire or be deleted — download immediately on receipt
(default). This reference SDK has no recovery path after the hot object expires.
"""

from __future__ import annotations

import logging
import os
import re
import time
from collections.abc import Callable
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Literal

import requests

from saucepan._paths import path_segment
from saucepan.campaigns import CampaignClient
from saucepan.landing_urls import LandingURLRejected, validate_landing_url

logger = logging.getLogger(__name__)

DownloadKind = Literal["graded", "raw", "both"]

DEFAULT_DATA_DIR = os.environ.get("SAUCEPAN_DATA_DIR", "./saucepan_deliveries")


@dataclass
class CampaignDelivery:
    """One graded frame delivery for a campaign creator."""

    id: str
    notification_id: str
    campaign_id: str
    status: str
    task_id: str | None = None
    task_public_id: str | None = None
    upload_id: str | None = None
    failure_reason: str | None = None
    raw_download_url: str | None = field(default=None, repr=False)
    graded_download_url: str | None = field(default=None, repr=False)
    fits_url: str | None = field(default=None, repr=False)
    points_earned: float | None = None
    stack_eligible: bool | None = None
    telescope_id: str | None = None
    created_at: str | None = None
    # Filled when auto-download (or explicit download_*) succeeds.
    local_graded_path: str | None = field(default=None, repr=False)
    local_raw_path: str | None = field(default=None, repr=False)
    _timeout: float = 300.0

    @classmethod
    def from_dict(cls, data: dict[str, Any], *, timeout: float = 300.0) -> CampaignDelivery:
        tel = data.get("telescope_id")
        return cls(
            id=str(data.get("id") or data.get("notification_id", "")),
            notification_id=str(data.get("notification_id") or data.get("id", "")),
            campaign_id=str(data.get("campaign_id", "")),
            status=str(data.get("status", "")),
            task_id=(str(data["task_id"]) if data.get("task_id") is not None else None),
            task_public_id=data.get("task_public_id"),
            upload_id=data.get("upload_id"),
            failure_reason=data.get("failure_reason"),
            raw_download_url=data.get("raw_download_url"),
            graded_download_url=data.get("graded_download_url") or data.get("fits_url"),
            fits_url=data.get("fits_url") or data.get("graded_download_url"),
            points_earned=data.get("points_earned"),
            stack_eligible=data.get("stack_eligible"),
            telescope_id=str(tel) if tel else None,
            created_at=data.get("created_at"),
            _timeout=timeout,
        )

    @property
    def local_path(self) -> str | None:
        """Preferred local file (graded, else raw) after download."""
        return self.local_graded_path or self.local_raw_path

    def download_raw(self, directory: str | Path) -> str:
        path = _download_url(self.raw_download_url, directory, self.id, "raw", self._timeout)
        self.local_raw_path = path
        return path

    def download_graded(self, directory: str | Path) -> str:
        url = self.graded_download_url or self.fits_url
        path = _download_url(url, directory, self.id, "graded", self._timeout)
        self.local_graded_path = path
        return path

    def download(
        self,
        directory: str | Path,
        *,
        kind: DownloadKind = "graded",
    ) -> str | tuple[str, str]:
        """Download graded and/or raw into *directory*; records local_* paths."""
        if kind == "both":
            return self.download_graded(directory), self.download_raw(directory)
        if kind == "raw":
            return self.download_raw(directory)
        return self.download_graded(directory)


class CampaignDeliveryInbox:
    """Poll ``GET /api/v1/inbox`` for campaign FITS deliveries."""

    def __init__(self, client: CampaignClient) -> None:
        self._client = client

    def poll(
        self,
        *,
        since: str | None = None,
        campaign_id: str | None = None,
    ) -> list[CampaignDelivery]:
        params: dict[str, str] = {}
        if since:
            params["since"] = since
        if campaign_id:
            params["campaign_id"] = campaign_id
        data = self._client._request("GET", "/api/v1/inbox", params=params)
        return [
            CampaignDelivery.from_dict(d, timeout=self._client.timeout)
            for d in data.get("deliveries") or []
        ]

    def acknowledge(self, delivery_id: str) -> None:
        delivery_id = path_segment(delivery_id, name="delivery_id")
        self._client._request("POST", f"/api/v1/inbox/{delivery_id}/ack")

    def run_worker(
        self,
        on_delivery: Callable[[CampaignDelivery], None] | None = None,
        *,
        campaign_id: str | None = None,
        poll_interval: float = 60.0,
        stop_event: Any | None = None,
        ack_on_failure: bool = False,
        auto_download: bool = True,
        data_dir: str | Path | None = None,
        download_kind: DownloadKind = "graded",
    ) -> None:
        """
        Durable poll loop for multi-day campaigns.

        **Default:** download completed FITS to ``data_dir`` (or
        ``SAUCEPAN_DATA_DIR`` / ``./saucepan_deliveries``) **before**
        ``on_delivery`` and before ack. Hot storage may be deleted later; this
        reference SDK has no recovery path after expiry.

        Pass ``auto_download=False`` only if the callback downloads itself.
        Failed ``on_delivery`` / download errors are **not** acked by default
        so the delivery retries on the next poll.
        """
        dest = Path(data_dir or DEFAULT_DATA_DIR)
        cursor: str | None = None
        while True:
            if stop_event is not None and getattr(stop_event, "is_set", lambda: False)():
                return
            try:
                deliveries = self.poll(since=cursor, campaign_id=campaign_id)
            except Exception as exc:
                logger.exception("inbox poll failed: %s", exc)
                deliveries = []

            for delivery in deliveries:
                try:
                    if auto_download and delivery.status == "completed":
                        delivery.download(dest, kind=download_kind)
                        logger.info(
                            "downloaded delivery %s -> %s",
                            delivery.id,
                            delivery.local_path,
                        )
                    if on_delivery is not None:
                        on_delivery(delivery)
                except Exception:
                    logger.exception("on_delivery/download failed for %s", delivery.id)
                    if ack_on_failure:
                        self.acknowledge(delivery.id)
                    continue
                self.acknowledge(delivery.id)
                if delivery.created_at:
                    cursor = delivery.created_at

            if stop_event is not None:
                if stop_event.wait(poll_interval):
                    return
            else:
                time.sleep(poll_interval)


def _download_url(
    url: str | None,
    directory: str | Path,
    delivery_id: str,
    suffix: str,
    timeout: float,
) -> str:
    if not url:
        raise ValueError(f"No download URL for {suffix} delivery {delivery_id}")
    try:
        url = validate_landing_url(url)
    except LandingURLRejected as exc:
        raise ValueError(str(exc)) from exc
    dest_dir = Path(directory)
    dest_dir.mkdir(parents=True, exist_ok=True)
    safe_id = re.sub(r"[^A-Za-z0-9._-]+", "_", str(delivery_id)).strip("._")
    safe_id = safe_id or "delivery"
    dest = dest_dir / f"{safe_id}_{suffix}.fits"
    response = requests.get(url, stream=True, timeout=timeout, allow_redirects=False)
    if response.status_code in (404, 410, 403):
        raise RuntimeError(
            f"Hot download unavailable for {suffix} delivery {delivery_id} "
            f"(HTTP {response.status_code}). Keep auto_download on so files are "
            f"saved while the URL is still live."
        )
    if not response.ok:
        raise RuntimeError(f"Download failed: HTTP {response.status_code}")
    with open(dest, "wb") as fh:
        for chunk in response.iter_content(chunk_size=65536):
            if chunk:
                fh.write(chunk)
    return str(dest)
