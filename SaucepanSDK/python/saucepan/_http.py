"""
Internal HTTP layer for the Saucepan Python SDK.

All public methods build on _request(), which handles:
  - X-API-Key header injection
  - JSON serialisation / deserialisation
  - HTTP status code → typed exception mapping

Retry on transient failures (5xx in _RETRY_STATUS_CODES, plus connection/read
errors) is handled by urllib3's Retry, mounted once on the session in __init__.
There is exactly one retry layer — _request() makes a single call and inspects
the already-retried result. Retry count and backoff come from the max_retries /
retry_backoff_base constructor args (default: 3 retries, exponential backoff).
"""

import os
from urllib.parse import urlparse

import requests
from requests.adapters import HTTPAdapter
from urllib3.util.retry import Retry

import saucepan.exceptions as exceptions
import saucepan.landing_urls as landing_urls
import saucepan.models as models
from saucepan._paths import path_segment

_RETRY_STATUS_CODES = {500, 502, 503, 504}
_MAX_RETRIES = 3
_RETRY_BACKOFF_BASE = 1.5


def _validate_transport(base_url: str) -> None:
    """Require HTTPS for requests to non-local hosts."""
    parsed = urlparse(base_url.strip())
    if parsed.scheme.lower() != "http":
        return
    host = (parsed.hostname or "").lower()
    if host not in {"127.0.0.1", "localhost", "::1"}:
        raise exceptions.ConfigurationError("API base URL must use HTTPS for non-local hosts")


class _HttpSession:
    def __init__(
        self,
        api_key: str,
        base_url: str,
        timeout: float = 30.0,
        pool_connections: int = 10,
        pool_maxsize: int = 10,
        max_retries: int = _MAX_RETRIES,
        retry_backoff_base: float = _RETRY_BACKOFF_BASE,
    ) -> None:
        self._base_url = base_url.rstrip("/")
        _validate_transport(self._base_url)
        self._timeout = timeout
        self._max_retries = max_retries
        self._retry_backoff_base = retry_backoff_base

        self._session = requests.Session()
        self._session.headers.update(
            {
                "X-API-Key": api_key,
                "Content-Type": "application/json",
                "Accept": "application/json",
            }
        )

        # Single retry layer for the whole SDK. total caps the overall count;
        # connect/read make it explicit that dropped connections and read
        # timeouts are retried too, not just bad status codes. allowed_methods
        # =None retries every verb (including POST), matching the method-blind
        # hand-rolled loop this replaced. raise_on_status=False hands the final
        # 5xx back to _request so it can raise a typed ServerError.
        retry_strategy = Retry(
            total=max_retries,
            connect=max_retries,
            read=max_retries,
            status_forcelist=list(_RETRY_STATUS_CODES),
            allowed_methods=None,
            backoff_factor=retry_backoff_base,
            raise_on_status=False,
        )

        adapter = HTTPAdapter(
            max_retries=retry_strategy,
            pool_connections=pool_connections,
            pool_maxsize=pool_maxsize,
        )

        self._session.mount("http://", adapter)
        self._session.mount("https://", adapter)
        # Signed object URLs must never receive the task-server API key.
        self._download_session = requests.Session()

    # -------------------------------------------------------------------------
    # Tasks
    # -------------------------------------------------------------------------

    def submit_task(self, spec):
        data = self._request("POST", "/tasks", json=spec.to_dict())
        return self._parse_task(data)

    def get_task(self, task_id):
        task_id = path_segment(task_id, name="task_id")
        data = self._request("GET", f"/tasks/{task_id}")
        return self._parse_task(data)

    def list_tasks(self, status=None, page=1, per_page=50):
        params = {"page": page, "per_page": per_page}
        if status:
            params["status"] = status
        data = self._request("GET", "/tasks", params=params)
        if isinstance(data, list):
            items = data
        else:
            items = data.get("tasks", [])
        return [self._parse_task(t) for t in items]

    # -------------------------------------------------------------------------
    # Inbox
    # -------------------------------------------------------------------------

    def poll_inbox(self):
        data = self._request("GET", "/inbox")
        return [self._parse_delivery(d) for d in data.get("deliveries", [])]

    def poll_inbox_all(self):
        data = self._request("GET", "/inbox", params={"all": "true"})
        return [self._parse_delivery(d) for d in data.get("deliveries", [])]

    def acknowledge_notification(self, notification_id):
        notification_id = path_segment(notification_id, name="notification_id")
        self._request("PATCH", f"/inbox/{notification_id}", json={"acknowledged": True})

    # -------------------------------------------------------------------------
    # Downloads
    # -------------------------------------------------------------------------

    def get_download_url(self, task_id):
        task_id = path_segment(task_id, name="task_id")
        data = self._request(
            "GET", f"/tasks/{task_id}/download-url"
        )
        return data["url"]

    def download_fits(self, task_id, fits_url, directory):
        """
        Stream the FITS file at fits_url to directory/<task_id>.fits.
        Returns the full local path of the saved file.
        """
        os.makedirs(directory, exist_ok=True)
        task_stem = os.path.basename(os.path.normpath(str(task_id)))
        if task_stem in ("", ".", ".."):
            task_stem = "task"
        dest = os.path.join(directory, f"{task_stem}.fits")

        try:
            fits_url = landing_urls.validate_landing_url(fits_url)
        except landing_urls.LandingURLRejected as exc:
            raise exceptions.ValidationError(str(exc)) from exc

        response = self._download_session.get(
            fits_url,
            stream=True,
            timeout=300,
            allow_redirects=False,
        )
        if not response.ok:
            raise exceptions.ServerError(f"Download failed: HTTP {response.status_code}")

        with open(dest, "wb") as f:
            for chunk in response.iter_content(chunk_size=65536):
                if chunk:
                    f.write(chunk)

        return dest

    # -------------------------------------------------------------------------
    # Developer / quota
    # -------------------------------------------------------------------------

    def get_quota(self):
        data = self._request("GET", "/me/quota")
        return models.QuotaStatus(
            total=data["quota_total"],
            used=data["quota_used"],
        )

    # -------------------------------------------------------------------------
    # Internal
    # -------------------------------------------------------------------------

    def _request(self, method, path, params=None, json=None):
        """Make one request and map the (already-retried) result to a value/exception.

        urllib3's Retry (mounted in __init__) has already retried transient 5xx
        and connection/read failures up to self._max_retries times with
        exponential backoff before control reaches here. If the status is still
        retryable at this point, every attempt failed — raise ServerError.
        """
        url = f"{self._base_url}{path}"

        try:
            response = self._session.request(
                method,
                url,
                params=params,
                json=json,
                timeout=30,
                allow_redirects=False,
            )
        except requests.ConnectionError as e:
            raise exceptions.ServerError("Connection failed") from e
        except requests.Timeout:
            raise exceptions.ServerError("Request timed out")

        if response.status_code in _RETRY_STATUS_CODES:
            raise exceptions.ServerError(
                f"Server error after {self._max_retries + 1} attempts: "
                f"HTTP {response.status_code}",
                status_code=response.status_code,
            )

        return self._handle_response(response)

    def _handle_response(self, response):
        """Map HTTP status codes to typed exceptions. Returns parsed JSON body on success."""
        if response.status_code in (200, 201, 202):
            if response.content:
                return response.json()
            return {}

        body = {}
        try:
            body = response.json()
        except Exception:
            pass

        if response.status_code == 401:
            raise exceptions.AuthError(body.get("error", "Authentication failed"))

        if response.status_code == 403:
            raise exceptions.AuthError(body.get("error", "Forbidden"))

        if response.status_code == 404:
            raise exceptions.NotFoundError(body.get("error", "Not found"))

        if response.status_code == 422:
            raise exceptions.ValidationError(
                body.get("error", "Validation failed"),
                fields=body.get("fields", {}),
            )

        if response.status_code == 429:
            error_code = body.get("error", "")
            if error_code == "quota_exceeded":
                raise exceptions.QuotaError(
                    body.get("message", "Quota exceeded"),
                    quota_total=body.get("quota_total"),
                    quota_used=body.get("quota_used"),
                )
            retry_after = None
            try:
                retry_after = int(response.headers.get("Retry-After", 0))
            except (ValueError, TypeError):
                pass
            raise exceptions.RateLimitError(
                body.get("message", "Rate limit exceeded"),
                retry_after=retry_after,
            )

        raise exceptions.ServerError(
            body.get("error", f"Unexpected HTTP {response.status_code}"),
            status_code=response.status_code,
        )

    def _parse_task(self, data):
        spec = models.TaskSpec(
            name=data["name"],
            integration_time=data["integration_time"],
            filters=data.get("required_filters") or [],
            min_power=data.get("min_power", 0.5),
            max_psf_fwhm=data.get("max_psf_fwhm"),
            max_plate_scale=data.get("max_plate_scale"),
            min_aperture_mm=data.get("min_aperture_mm"),
            priority=data.get("priority", 10),
            description=data.get("description", ""),
        )
        task_id = data.get("id") or data.get("developer_task_id")
        return models.Task(
            id=str(task_id),
            name=data["name"],
            status=data["status"],
            spec=spec,
        )

    def _parse_delivery(self, data):
        spec_data = data.get("original_spec", {})
        spec = models.TaskSpec(
            name=spec_data.get("name", ""),
            integration_time=spec_data.get("integration_time", 0),
            filters=spec_data.get("required_filters") or [],
            min_power=spec_data.get("min_power", 0.5),
            max_psf_fwhm=spec_data.get("max_psf_fwhm"),
            max_plate_scale=spec_data.get("max_plate_scale"),
            min_aperture_mm=spec_data.get("min_aperture_mm"),
            priority=spec_data.get("priority", 10),
            description=spec_data.get("description", ""),
        )
        notif_id = data["notification_id"]
        if not isinstance(notif_id, int):
            notif_id = int(notif_id)
        return models.Delivery(
            notification_id=notif_id,
            task_id=str(data["task_id"]),
            status=data["status"],
            failure_reason=data.get("failure_reason"),
            _original_spec=spec,
            _fits_url=data.get("fits_url"),
            _http=self,
        )
