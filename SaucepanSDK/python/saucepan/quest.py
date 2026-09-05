"""
Dev helper: submit tasks to task-server ``POST /quest/tasks``.

The public SDK ``Client`` targets ``/api/v1/tasks``.
Use ``QuestClient`` when submitting tasks to a local task server during pier dev.
"""

from __future__ import annotations

from typing import Any

import requests

from saucepan._http import _validate_transport
from saucepan._paths import path_segment
from saucepan.exceptions import ServerError
from saucepan.models import TaskSpec

DEFAULT_QUEST_API_URL = "http://127.0.0.1:8080"


class QuestClient:
    """Submit tasks to Saucepan task-server ``/quest/tasks`` (no API key)."""

    def __init__(self, base_url: str = DEFAULT_QUEST_API_URL, timeout: float = 30.0) -> None:
        self.base_url = base_url.rstrip("/")
        _validate_transport(self.base_url)
        self.timeout = timeout

    def health(self) -> bool:
        try:
            response = requests.get(
                f"{self.base_url}/",
                timeout=self.timeout,
                allow_redirects=False,
            )
            return response.ok
        except requests.RequestException:
            return False

    def submit(
        self,
        spec: TaskSpec,
        *,
        target_ra: float | None = None,
        target_dec: float | None = None,
        min_altitude_deg: float | None = None,
        allow_emulator: bool = False,
    ) -> dict[str, Any]:
        """
        Validate *spec* with SDK rules, then insert via the task API.

        Returns:
            ``{"id": <uuid str>, "name": <str>}``
        """
        spec.validate()
        payload = spec.to_dict()
        if target_ra is not None:
            payload["target_ra"] = target_ra
        if target_dec is not None:
            payload["target_dec"] = target_dec
        if min_altitude_deg is not None:
            payload["min_altitude_deg"] = min_altitude_deg
        payload["allow_emulator"] = allow_emulator

        try:
            response = requests.post(
                f"{self.base_url}/quest/tasks",
                json=payload,
                timeout=self.timeout,
                allow_redirects=False,
            )
        except requests.RequestException as exc:
            raise ServerError("Connection failed") from exc

        if response.status_code not in (200, 201):
            body: dict[str, Any] = {}
            try:
                body = response.json()
            except Exception:
                pass
            raise ServerError(
                body.get("error", f"Unexpected HTTP {response.status_code}"),
                status_code=response.status_code,
            )

        data = response.json()
        task = data.get("task") or {}
        task_id = task.get("id")
        if task_id is None:
            raise ServerError("Task created but response missing task.id")

        return {"id": str(task_id), "name": spec.name}

    def get(self, task_id: str) -> dict[str, Any] | None:
        task_id = path_segment(task_id, name="task_id")
        try:
            response = requests.get(
                f"{self.base_url}/quest/tasks/{task_id}",
                timeout=self.timeout,
                allow_redirects=False,
            )
        except requests.RequestException as exc:
            raise ServerError("Connection failed") from exc

        if not response.ok:
            raise ServerError(
                f"GET task failed: HTTP {response.status_code}",
                status_code=response.status_code,
            )

        data = response.json()
        return data.get("task")
