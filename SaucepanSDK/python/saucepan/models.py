from dataclasses import dataclass, field
from typing import TYPE_CHECKING, Any

import saucepan.exceptions as exceptions

if TYPE_CHECKING:
    from saucepan._http import _HttpSession


@dataclass
class TaskSpec:
    """
    Describes an observation request submitted to the Saucepan network.

    Required:
        name, integration_time, min_power

    Optional — set as many or as few as needed:
        normalized_integration_budget_s  2 m-equivalent science budget (seconds)
        filters          list of filter names, e.g. ["R", "G", "B"]
        max_psf_fwhm     maximum acceptable PSF FWHM in arcseconds
        max_plate_scale  maximum acceptable plate scale in arcsec/pixel (lower = finer resolution)
        min_aperture_mm  minimum telescope aperture in mm
        priority         scheduling priority 1–50 (clamped server-side), default 10

    min_power is the catch-all: used after specific requirements (filters, PSF, plate scale,
    aperture) have already filtered the telescope pool. Always required.
    """

    name: str
    integration_time: float
    min_power: float

    filters: list[str] | None = None
    max_psf_fwhm: float | None = None
    max_plate_scale: float | None = None
    min_aperture_mm: float | None = None
    normalized_integration_budget_s: float | None = None
    priority: int = 10
    description: str = ""

    def validate(self) -> None:
        errors = {}
        if not self.name:
            errors["name"] = "required"
        if self.integration_time <= 0:
            errors["integration_time"] = "must be > 0"
        if not (0.0 <= self.min_power <= 1.0):
            errors["min_power"] = "must be between 0.0 and 1.0"
        if self.max_psf_fwhm is not None and self.max_psf_fwhm <= 0:
            errors["max_psf_fwhm"] = "must be > 0"
        if self.max_plate_scale is not None and self.max_plate_scale <= 0:
            errors["max_plate_scale"] = "must be > 0"
        if self.min_aperture_mm is not None and self.min_aperture_mm <= 0:
            errors["min_aperture_mm"] = "must be > 0"
        if (
            self.normalized_integration_budget_s is not None
            and self.normalized_integration_budget_s <= 0
        ):
            errors["normalized_integration_budget_s"] = "must be > 0"
        if errors:
            raise exceptions.ValidationError("Invalid task spec", fields=errors)

    def to_dict(self) -> dict[str, Any]:
        d = {
            "name": self.name,
            "description": self.description,
            "integration_time": self.integration_time,
            "min_power": self.min_power,
            "priority": self.priority,
        }
        if self.filters is not None:
            d["required_filters"] = self.filters
        if self.max_psf_fwhm is not None:
            d["max_psf_fwhm"] = self.max_psf_fwhm
        if self.max_plate_scale is not None:
            d["max_plate_scale"] = self.max_plate_scale
        if self.min_aperture_mm is not None:
            d["min_aperture_mm"] = self.min_aperture_mm
        if self.normalized_integration_budget_s is not None:
            d["normalized_integration_budget_s"] = self.normalized_integration_budget_s
        return d


@dataclass
class Task:
    """Returned immediately after submission. Holds the task ID for later inbox lookup."""

    id: str
    name: str
    status: str
    spec: TaskSpec

    @property
    def is_pending(self) -> bool:
        return self.status in ("pending", "assigned", "in_progress")

    @property
    def is_complete(self) -> bool:
        return self.status == "completed"

    @property
    def is_failed(self) -> bool:
        return self.status == "failed"


@dataclass
class Delivery:
    """
    A notification in the developer's inbox.
    Self-contained: carries the original spec and result so no extra lookups are needed.
    """

    notification_id: int
    task_id: str
    status: str
    failure_reason: str | None
    _original_spec: TaskSpec
    _fits_url: str | None = field(repr=False)
    _http: "_HttpSession" = field(repr=False)
    local_path: str | None = None

    @property
    def fits_url(self) -> str:
        if self.status != "completed":
            raise exceptions.SaucepanError("Task did not complete — no FITS file available")
        if self._fits_url is None:
            raise exceptions.SaucepanError("FITS URL not available")
        return self._fits_url

    def download(self, directory: str) -> str:
        if self.status != "completed":
            raise exceptions.SaucepanError("Task did not complete — nothing to download")
        path = self._http.download_fits(self.task_id, self._fits_url, directory)
        self.local_path = path
        return path

    def acknowledge(self) -> None:
        self._http.acknowledge_notification(self.notification_id)

    def resubmit(self) -> "Task":
        return self._http.submit_task(self._original_spec)


@dataclass
class QuotaStatus:
    total: int
    used: int

    @property
    def remaining(self) -> int:
        return self.total - self.used

    @property
    def is_exhausted(self) -> bool:
        return self.remaining <= 0
