"""
Researcher campaign authoring against task-server ``/api/v1/campaigns``.

Requires a JWT from ``POST /auth/login`` for an approved researcher account.
Use ``CampaignClient`` during pier/dev against the Go apiserver on port 8080.

Season model: one long-lived task per (target × filter) with a large
``normalized_integration_budget_s``. Handoff/relay is server-side — not SDK.
"""

from __future__ import annotations

import json
import logging
import time
from collections.abc import Callable
from dataclasses import dataclass, field
from pathlib import Path
from typing import TYPE_CHECKING, Any

import requests

from saucepan._http import _validate_transport
from saucepan._paths import path_segment
from saucepan.exceptions import AuthError, ServerError, ValidationError

if TYPE_CHECKING:
    from saucepan.messageboard import CampaignBoard
from saucepan.models import TaskSpec

DEFAULT_API_URL = "http://127.0.0.1:8080"
logger = logging.getLogger(__name__)


@dataclass
class CampaignPack:
    """Campaign pack matching ``SaucepanServer/contracts/campaign_pack.schema.json``."""

    name: str
    targets: list[dict[str, Any]] = field(default_factory=list)
    test_only: bool = False
    description: str = ""
    hook_image_ref: str | None = None
    hook_placement: str = "none"
    comp_stars: list[dict[str, Any]] = field(default_factory=list)
    product: dict[str, Any] | None = None
    coverage: dict[str, Any] | None = None  # optional; default off
    season: dict[str, Any] | None = None  # continuous/sparse/TOO KPI metadata
    pier_code: dict[str, Any] | None = None

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> CampaignPack:
        cov = data.get("coverage")
        season = data.get("season")
        return cls(
            name=data.get("name", ""),
            targets=list(data.get("targets") or []),
            test_only=bool(data.get("test_only", False)),
            description=data.get("description", "") or "",
            hook_image_ref=data.get("hook_image_ref"),
            hook_placement=data.get("hook_placement") or "none",
            comp_stars=list(data.get("comp_stars") or []),
            product=dict(data["product"]) if isinstance(data.get("product"), dict) else None,
            coverage=dict(cov) if isinstance(cov, dict) else None,
            season=dict(season) if isinstance(season, dict) else None,
            pier_code=dict(data["pier_code"]) if isinstance(data.get("pier_code"), dict) else None,
        )

    @classmethod
    def from_json_file(cls, path: str | Path) -> CampaignPack:
        raw = Path(path).read_text(encoding="utf-8")
        return cls.from_dict(json.loads(raw))

    def to_dict(self) -> dict[str, Any]:
        d: dict[str, Any] = {"name": self.name, "targets": self.targets}
        if self.test_only:
            d["test_only"] = True
        if self.description:
            d["description"] = self.description
        if self.hook_image_ref:
            d["hook_image_ref"] = self.hook_image_ref
        if self.hook_placement and self.hook_placement != "none":
            d["hook_placement"] = self.hook_placement
        if self.comp_stars:
            d["comp_stars"] = self.comp_stars
        if self.product:
            d["product"] = self.product
        if self.coverage:
            d["coverage"] = self.coverage
        if self.season:
            d["season"] = self.season
        if self.pier_code:
            d["pier_code"] = self.pier_code
        return d

    def validate(self, *, for_publish: bool = False) -> None:
        errors: dict[str, str] = {}
        if not self.name.strip():
            errors["name"] = "required"
        _validate_pack_metadata(self.coverage, self.season, errors)
        if for_publish:
            _validate_publish_targets(self.targets, errors)
        if errors:
            raise ValidationError("Invalid campaign pack", fields=errors)


def _validate_pack_metadata(
    coverage: dict[str, Any] | None,
    season: dict[str, Any] | None,
    errors: dict[str, str],
) -> None:
    """Validate optional coverage and season metadata in one place."""
    if coverage:
        mode = str(coverage.get("mode") or "soft")
        if mode not in ("soft", "hard"):
            errors["coverage.mode"] = "must be soft or hard"
        span = coverage.get("min_longitude_span_deg")
        if span is not None and (float(span) < 0 or float(span) > 360):
            errors["coverage.min_longitude_span_deg"] = "must be in [0, 360]"
    if season:
        kind = str(season.get("kind") or "continuous")
        if kind not in ("continuous", "sparse", "too"):
            errors["season.kind"] = "must be continuous, sparse, or too"
        urgency = str(season.get("urgency") or "normal")
        if urgency not in ("normal", "elevated", "critical"):
            errors["season.urgency"] = "must be normal, elevated, or critical"
        duty_cycle = season.get("target_duty_cycle")
        if duty_cycle is not None and (float(duty_cycle) < 0 or float(duty_cycle) > 1):
            errors["season.target_duty_cycle"] = "must be in [0, 1]"


def _validate_publish_targets(
    targets: list[dict[str, Any]],
    errors: dict[str, str],
) -> None:
    """Validate the target fields required when a pack is published."""
    if not targets:
        errors["targets"] = "at least one target required to publish"
    for index, target in enumerate(targets):
        prefix = f"targets[{index}]"
        if not target.get("filters"):
            errors[f"{prefix}.filters"] = "required"
        if not target.get("exposure_sec") or float(target["exposure_sec"]) <= 0:
            errors[f"{prefix}.exposure_sec"] = "must be > 0"
        if not target.get("frame_count") or int(target["frame_count"]) <= 0:
            errors[f"{prefix}.frame_count"] = "must be > 0"

        saturation = target.get("saturation")
        if not isinstance(saturation, dict):
            continue
        strategy = saturation.get("strategy")
        if strategy and strategy not in ("short", "defocus", "nd", "manual"):
            errors[f"{prefix}.saturation.strategy"] = "must be short, defocus, nd, or manual"
        max_exposure = saturation.get("max_exposure_sec")
        if max_exposure is not None and float(target.get("exposure_sec") or 0) > float(
            max_exposure
        ):
            errors[f"{prefix}.exposure_sec"] = "exceeds saturation.max_exposure_sec"


@dataclass
class ResearcherSession:
    """Access + refresh tokens for a months-long campaign brain."""

    access_token: str = field(repr=False)
    refresh_token: str = field(default="", repr=False)
    expires_at: str | None = None
    user_id: str | None = None
    email: str | None = field(default=None, repr=False)


@dataclass
class CampaignTask:
    """Typed campaign task with budget progress helpers."""

    id: str
    name: str
    status: str
    campaign_id: str = ""
    normalized_integration_budget_s: float = 0.0
    normalized_integration_earned_s: float = 0.0
    assigned_telescope_id: str | None = None
    target_ra: float | None = None
    target_dec: float | None = None
    priority: int = 0
    integration_time: float | None = None

    @classmethod
    def from_dict(cls, data: dict[str, Any], *, campaign_id: str = "") -> CampaignTask:
        assigned = data.get("assigned_telescope_id")
        return cls(
            id=str(data.get("id") or data.get("public_id") or ""),
            name=str(data.get("name", "")),
            status=str(data.get("status", "")),
            campaign_id=str(data.get("campaign_id") or campaign_id or ""),
            normalized_integration_budget_s=float(
                data.get("normalized_integration_budget_s") or 0.0
            ),
            normalized_integration_earned_s=float(
                data.get("normalized_integration_earned_s") or 0.0
            ),
            assigned_telescope_id=str(assigned) if assigned else None,
            target_ra=_opt_float(data.get("target_ra")),
            target_dec=_opt_float(data.get("target_dec")),
            priority=int(data.get("priority") or 0),
            integration_time=_opt_float(data.get("integration_time")),
        )

    @property
    def budget_remaining(self) -> float:
        return max(0.0, self.normalized_integration_budget_s - self.normalized_integration_earned_s)

    @property
    def pct_complete(self) -> float:
        if self.normalized_integration_budget_s <= 0:
            return 0.0
        return min(
            100.0,
            100.0 * self.normalized_integration_earned_s / self.normalized_integration_budget_s,
        )

    @property
    def is_budget_complete(self) -> bool:
        if self.normalized_integration_budget_s <= 0:
            return self.status == "completed"
        return self.normalized_integration_earned_s >= self.normalized_integration_budget_s


def login_researcher(
    base_url: str,
    username: str,
    password: str,
    *,
    timeout: float = 30.0,
) -> str:
    """
    Obtain a JWT access token via ``POST /auth/login``.

    *username* is the account login id (not an email address).

    Prefer :func:`login_researcher_session` for long-running workers that need refresh.
    """
    return login_researcher_session(base_url, username, password, timeout=timeout).access_token


def _post_auth(
    base_url: str,
    path: str,
    payload: dict[str, str],
    timeout: float,
) -> requests.Response:
    """POST an authentication payload while keeping transport errors uniform."""
    _validate_transport(base_url)
    try:
        return requests.post(
            f"{base_url.rstrip('/')}{path}",
            json=payload,
            timeout=timeout,
            allow_redirects=False,
        )
    except requests.RequestException as exc:
        raise ServerError("Connection failed") from exc


def login_researcher_session(
    base_url: str,
    username: str,
    password: str,
    *,
    timeout: float = 30.0,
) -> ResearcherSession:
    """Login and return access + refresh tokens for durable campaign brains."""
    response = _post_auth(
        base_url,
        "/auth/login",
        {"username": username, "password": password},
        timeout,
    )

    if response.status_code == 401:
        raise AuthError("Invalid username or password")
    if response.status_code == 403:
        body = _safe_json(response)
        raise AuthError(body.get("error", "Forbidden"))
    if not response.ok:
        body = _safe_json(response)
        raise ServerError(
            body.get("error", f"Login failed: HTTP {response.status_code}"),
            status_code=response.status_code,
        )

    data = response.json()
    token = data.get("access_token")
    if not token:
        raise ServerError("Login response missing access_token")
    user = data.get("user") or {}
    return ResearcherSession(
        access_token=str(token),
        refresh_token=str(data.get("refresh_token") or ""),
        expires_at=data.get("expires_at"),
        user_id=user.get("id"),
        email=user.get("email") or user.get("username") or username,
    )


def refresh_researcher_session(
    base_url: str,
    refresh_token: str,
    *,
    timeout: float = 30.0,
) -> ResearcherSession:
    """Exchange a refresh token for a new access + refresh pair."""
    if not refresh_token:
        raise AuthError("refresh_token is required")
    response = _post_auth(
        base_url,
        "/auth/refresh",
        {"refresh_token": refresh_token},
        timeout,
    )

    if response.status_code in (401, 403):
        raise AuthError(_safe_json(response).get("error", "Invalid or expired refresh token"))
    if not response.ok:
        body = _safe_json(response)
        raise ServerError(
            body.get("error", f"Refresh failed: HTTP {response.status_code}"),
            status_code=response.status_code,
        )

    data = response.json()
    token = data.get("access_token")
    if not token:
        raise ServerError("Refresh response missing access_token")
    user = data.get("user") or {}
    return ResearcherSession(
        access_token=str(token),
        refresh_token=str(data.get("refresh_token") or refresh_token),
        expires_at=data.get("expires_at"),
        user_id=user.get("id"),
        email=user.get("email"),
    )


class CampaignClient:
    """Create and publish campaigns on ``/api/v1/campaigns`` (researcher JWT)."""

    def __init__(
        self,
        base_url: str = DEFAULT_API_URL,
        access_token: str = "",
        timeout: float = 30.0,
        *,
        refresh_token: str = "",
    ) -> None:
        _validate_transport(base_url)
        self.base_url = base_url.rstrip("/")
        self.access_token = access_token
        self.refresh_token = refresh_token
        self.timeout = timeout
        self.alerts = TextInbox(self, "alerts")
        self.updates = TextInbox(self, "updates")
        from saucepan.campaign_inbox import CampaignDeliveryInbox

        self.deliveries = CampaignDeliveryInbox(self)

    @classmethod
    def from_session(
        cls,
        base_url: str,
        session: ResearcherSession,
        *,
        timeout: float = 30.0,
    ) -> CampaignClient:
        return cls(
            base_url,
            access_token=session.access_token,
            refresh_token=session.refresh_token,
            timeout=timeout,
        )

    def refresh_auth(self) -> ResearcherSession:
        """Refresh access token in-place using the stored refresh token."""
        session = refresh_researcher_session(
            self.base_url, self.refresh_token, timeout=self.timeout
        )
        self.access_token = session.access_token
        if session.refresh_token:
            self.refresh_token = session.refresh_token
        return session

    def create(
        self,
        pack: CampaignPack | dict[str, Any] | str | Path,
    ) -> dict[str, Any]:
        """
        Create a draft campaign from a pack.

        Returns:
            ``{"id": <uuid>, "status": "draft"}``
        """
        payload = _normalize_pack(pack)
        payload.validate()

        data = self._request("POST", "/api/v1/campaigns", json=payload.to_dict())
        campaign = data.get("campaign") or {}
        if not campaign.get("id"):
            raise ServerError("Campaign created but response missing campaign.id")
        return {
            "id": str(campaign["id"]),
            "status": campaign.get("status", "draft"),
        }

    def publish(self, campaign_id: str) -> dict[str, Any]:
        """
        Publish a campaign (expand pack → tasks, set status active).

        Returns:
            ``{"id", "status", "tasks_created"}``
        """
        campaign_id = path_segment(campaign_id, name="campaign_id")
        data = self._request(
            "POST",
            f"/api/v1/campaigns/{campaign_id}/publish",
        )
        return _campaign_result(data, campaign_id, "active", include_task_count=True)

    def pause(self, campaign_id: str) -> dict[str, Any]:
        """Pause an active campaign (blocks new assigns; in-flight may finish)."""
        campaign_id = path_segment(campaign_id, name="campaign_id")
        data = self._request("POST", f"/api/v1/campaigns/{campaign_id}/pause")
        return _campaign_result(data, campaign_id, "paused")

    def resume(self, campaign_id: str) -> dict[str, Any]:
        """Resume a paused campaign."""
        campaign_id = path_segment(campaign_id, name="campaign_id")
        data = self._request("POST", f"/api/v1/campaigns/{campaign_id}/resume")
        return _campaign_result(data, campaign_id, "active")

    def get(self, campaign_id: str) -> dict[str, Any] | None:
        """Return campaign metadata or ``None`` if not found."""
        campaign_id = path_segment(campaign_id, name="campaign_id")
        data = self._request("GET", f"/api/v1/campaigns/{campaign_id}")
        return data.get("campaign")

    def list_tasks(self, campaign_id: str) -> list[CampaignTask]:
        """List tasks belonging to a campaign (typed with budget progress)."""
        campaign_id = path_segment(campaign_id, name="campaign_id")
        data = self._request("GET", f"/api/v1/campaigns/{campaign_id}/tasks")
        tasks = data.get("tasks") or []
        return [CampaignTask.from_dict(dict(t), campaign_id=campaign_id) for t in tasks]

    def stack_status(self, campaign_id: str) -> dict[str, Any]:
        """Per-task stack-eligible frame counts for a campaign."""
        campaign_id = path_segment(campaign_id, name="campaign_id")
        return self._request("GET", f"/api/v1/campaigns/{campaign_id}/stack-status")

    def set_coverage(
        self,
        campaign_id: str,
        *,
        enabled: bool,
        n_main: int = 1,
        redundancy: bool = False,
        max_gap_min: int | None = None,
        max_sites: int | None = None,
        mode: str | None = None,
        preferred_sites: list[str] | None = None,
        min_sites: int | None = None,
        min_longitude_span_deg: float | None = None,
    ) -> dict[str, Any]:
        """Persist coverage intent on the campaign pack (default off until enabled)."""
        campaign_id = path_segment(campaign_id, name="campaign_id")
        payload: dict[str, Any] = {
            "enabled": enabled,
            "n_main": n_main,
            "redundancy": redundancy,
        }
        if max_gap_min is not None:
            payload["max_gap_min"] = max_gap_min
        if max_sites is not None:
            payload["max_sites"] = max_sites
        if mode is not None:
            payload["mode"] = mode
        if preferred_sites is not None:
            payload["preferred_sites"] = list(preferred_sites)
        if min_sites is not None:
            payload["min_sites"] = min_sites
        if min_longitude_span_deg is not None:
            payload["min_longitude_span_deg"] = min_longitude_span_deg
        return self._request(
            "POST",
            f"/api/v1/campaigns/{campaign_id}/coverage",
            json=payload,
        )

    def preview_coverage(
        self,
        campaign_id: str,
        coverage: dict[str, Any] | None = None,
    ) -> dict[str, Any]:
        """Read-only greedy site plan (does not mutate assignment)."""
        campaign_id = path_segment(campaign_id, name="campaign_id")
        return self._request(
            "POST",
            f"/api/v1/campaigns/{campaign_id}/coverage/preview",
            json=coverage or {},
        )

    def apply_coverage(
        self,
        campaign_id: str,
        coverage: dict[str, Any] | None = None,
    ) -> dict[str, Any]:
        """
        Persist coverage intent (if body given) and open handoff windows on tasks.

        Site selection remains server-authoritative; this does not create per-site tasks.
        Hard mode may return 409 when gates fail.
        """
        campaign_id = path_segment(campaign_id, name="campaign_id")
        return self._request(
            "POST",
            f"/api/v1/campaigns/{campaign_id}/coverage/apply",
            json=coverage,
        )

    def coverage_status(
        self,
        campaign_id: str,
        *,
        bin_minutes: float | None = None,
    ) -> dict[str, Any]:
        """Realized coverage KPIs from server frame grades."""
        campaign_id = path_segment(campaign_id, name="campaign_id")
        path = f"/api/v1/campaigns/{campaign_id}/coverage/status"
        params = {"bin_minutes": str(bin_minutes)} if bin_minutes is not None else None
        return self._request("GET", path, params=params)

    def fleet_sites(self) -> dict[str, Any]:
        """Fleet geography and optics inventory for coverage planning."""
        return self._request("GET", "/api/v1/fleet/sites")

    def account_usage(
        self,
        *,
        since: str | None = None,
        until: str | None = None,
    ) -> dict[str, Any]:
        """Researcher usage derived from graded frames on owned campaigns.

        Optional ``since`` / ``until`` are RFC3339 timestamps filtering grade
        ``created_at``. Tracker only — does not enforce publish quotas.
        """
        params: dict[str, str] = {}
        if since:
            params["since"] = since
        if until:
            params["until"] = until
        return self._request(
            "GET",
            "/api/v1/account/usage",
            params=params or None,
        )

    def coverage_metrics_from_deliveries(
        self,
        deliveries: list[Any],
        pack: CampaignPack | dict[str, Any] | None = None,
        *,
        telescope_longitudes: dict[str, float] | None = None,
        **kwargs: Any,
    ) -> Any:
        """Local coverage metrics helper (survives inbox ack). See coverage_metrics."""
        from saucepan.coverage_metrics import (
            compute_coverage_metrics,
            metrics_from_pack,
        )

        if pack is not None:
            return metrics_from_pack(
                deliveries,
                pack,
                telescope_longitudes=telescope_longitudes,
                **kwargs,
            )
        return compute_coverage_metrics(
            deliveries,
            telescope_longitudes=telescope_longitudes,
            **kwargs,
        )

    def board(self, campaign_id: str) -> CampaignBoard:
        """The campaign's messageboard (read/write over HTTP).

        The pier side is the retained MQTT board; this is the researcher's
        way in without an MQTT credential.
        """
        from saucepan.messageboard import CampaignBoard

        return CampaignBoard(self, path_segment(campaign_id, name="campaign_id"))

    def add_task(
        self,
        campaign_id: str,
        spec: TaskSpec,
        *,
        target_ra: float | None = None,
        target_dec: float | None = None,
        allow_emulator: bool = False,
    ) -> dict[str, Any]:
        """
        Add a task to an existing campaign folder.

        ``normalized_integration_budget_s`` must be set on *spec*.
        For seasons, prefer one task per (target × filter) — do not add nightly tasks.
        """
        campaign_id = path_segment(campaign_id, name="campaign_id")
        spec.validate()
        if spec.normalized_integration_budget_s is None:
            raise ValidationError(
                "normalized_integration_budget_s is required for campaign tasks",
                fields={"normalized_integration_budget_s": "required"},
            )
        payload = spec.to_dict()
        if target_ra is not None:
            payload["target_ra"] = target_ra
        if target_dec is not None:
            payload["target_dec"] = target_dec
        payload["allow_emulator"] = allow_emulator

        data = self._request(
            "POST",
            f"/api/v1/campaigns/{campaign_id}/tasks",
            json=payload,
        )
        task = data.get("task") or {}
        if not task.get("id"):
            raise ServerError("Task created but response missing task.id")
        return {"id": str(task["id"]), "campaign_id": str(task.get("campaign_id", campaign_id))}

    def _request(
        self,
        method: str,
        path: str,
        *,
        json: dict[str, Any] | None = None,
        params: dict[str, str] | None = None,
        _retried: bool = False,
    ) -> dict[str, Any]:
        if not self.access_token:
            raise AuthError("access_token is required for campaign API calls")

        headers = {
            "Authorization": f"Bearer {self.access_token}",
            "Content-Type": "application/json",
            "Accept": "application/json",
        }
        url = f"{self.base_url}{path}"
        try:
            response = requests.request(
                method,
                url,
                json=json,
                params=params,
                headers=headers,
                timeout=self.timeout,
                allow_redirects=False,
            )
        except requests.RequestException as exc:
            raise ServerError("Connection failed") from exc

        if response.status_code == 401 and self.refresh_token and not _retried:
            try:
                self.refresh_auth()
            except AuthError:
                raise AuthError(_safe_json(response).get("error", "Authentication required"))
            return self._request(method, path, json=json, params=params, _retried=True)

        if response.status_code == 401:
            raise AuthError(_safe_json(response).get("error", "Authentication required"))
        if response.status_code == 403:
            raise AuthError(_safe_json(response).get("error", "Forbidden"))
        if response.status_code not in (200, 201):
            body = _safe_json(response)
            raise ServerError(
                body.get("error", f"Unexpected HTTP {response.status_code}"),
                status_code=response.status_code,
            )
        if not response.content:
            return {}
        return response.json()


def _campaign_result(
    data: dict[str, Any],
    campaign_id: str,
    default_status: str,
    *,
    include_task_count: bool = False,
) -> dict[str, Any]:
    """Shape the common campaign mutation response returned by the API."""
    campaign = data.get("campaign") or {}
    result: dict[str, Any] = {
        "id": str(campaign.get("id", campaign_id)),
        "status": campaign.get("status", default_status),
    }
    if include_task_count:
        result["tasks_created"] = int(campaign.get("tasks_created", 0))
    return result


class TextInbox:
    """Poll + ack text events for researchers (alerts or updates)."""

    def __init__(self, client: CampaignClient, channel: str) -> None:
        if channel not in ("alerts", "updates"):
            raise ValueError("channel must be 'alerts' or 'updates'")
        self._client = client
        self._channel = channel

    def poll(
        self,
        *,
        since: str | None = None,
        campaign_id: str | None = None,
    ) -> list[dict[str, Any]]:
        params: dict[str, str] = {}
        if since:
            params["since"] = since
        if campaign_id:
            params["campaign_id"] = campaign_id
        data = self._client._request("GET", f"/api/v1/{self._channel}", params=params)
        return list(data.get(self._channel) or [])

    def ack(self, event_id: str) -> None:
        event_id = path_segment(event_id, name="event_id")
        self._client._request("POST", f"/api/v1/{self._channel}/{event_id}/ack")

    def run_worker(
        self,
        on_event: Callable[[dict[str, Any]], None],
        *,
        campaign_id: str | None = None,
        poll_interval: float = 60.0,
        stop_event: Any | None = None,
        ack_on_failure: bool = False,
    ) -> None:
        """
        Durable poll loop for alerts or updates (multi-month safe).

        Failed callbacks are not acked by default so events retry on the next poll.
        """
        cursor: str | None = None
        while True:
            if stop_event is not None and getattr(stop_event, "is_set", lambda: False)():
                return
            try:
                events = self.poll(since=cursor, campaign_id=campaign_id)
            except Exception as exc:
                logger.exception("%s poll failed: %s", self._channel, exc)
                events = []

            for event in events:
                event_id = str(event.get("id") or "")
                try:
                    on_event(event)
                except Exception:
                    logger.exception("on_event failed for %s %s", self._channel, event_id)
                    if ack_on_failure and event_id:
                        self.ack(event_id)
                    continue
                if event_id:
                    self.ack(event_id)
                created = event.get("created_at")
                if created:
                    cursor = str(created)

            if stop_event is not None:
                if stop_event.wait(poll_interval):
                    return
            else:
                time.sleep(poll_interval)


def _normalize_pack(pack: CampaignPack | dict[str, Any] | str | Path) -> CampaignPack:
    if isinstance(pack, CampaignPack):
        return pack
    if isinstance(pack, (str, Path)):
        return CampaignPack.from_json_file(pack)
    return CampaignPack.from_dict(pack)


def _safe_json(response: requests.Response) -> dict[str, Any]:
    try:
        return response.json()
    except Exception:
        return {}


def _opt_float(value: Any) -> float | None:
    if value is None:
        return None
    try:
        return float(value)
    except (TypeError, ValueError):
        return None
