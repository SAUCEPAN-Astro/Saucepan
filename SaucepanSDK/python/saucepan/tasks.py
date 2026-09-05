import time

import saucepan.models as models


class TasksResource:
    def __init__(self, http):
        self._http = http

    def submit(
        self,
        name,
        integration_time,
        min_power,
        filters=None,
        max_psf_fwhm=None,
        max_plate_scale=None,
        min_aperture_mm=None,
        priority=10,
        description="",
    ):
        """
        Submit an observation task to the network. Returns immediately with a Task ID.
        The task will be picked up by a telescope when one is available.

        Args:
            name:             Human-readable target name (e.g. "M42", "NGC 891")
            integration_time: Exposure time in seconds
            min_power:        Required. Telescope capability 0.0–1.0. Catch-all filter
                              applied after specific requirements narrow the pool.
            filters:          Optional list of filter names, e.g. ["R", "G", "B"]
            max_psf_fwhm:     Optional. Maximum acceptable PSF FWHM in arcseconds
            max_plate_scale:  Optional. Maximum plate scale in arcsec/pixel (lower = finer)
            min_aperture_mm:  Optional. Minimum telescope aperture in mm
            priority:         Scheduling priority 1–50, default 10 (clamped server-side)
            description:      Optional notes

        Returns:
            Task with id, name, status="pending"

        Raises:
            ValidationError   if spec fields are invalid
            QuotaError        if developer quota is exhausted
            RateLimitError    if rate limit is exceeded
            AuthError         if API key is invalid or revoked
        """
        spec = models.TaskSpec(
            name=name,
            integration_time=integration_time,
            min_power=min_power,
            filters=filters,
            max_psf_fwhm=max_psf_fwhm,
            max_plate_scale=max_plate_scale,
            min_aperture_mm=min_aperture_mm,
            priority=priority,
            description=description,
        )
        spec.validate()
        return self._http.submit_task(spec)

    def get(self, task_id):
        """
        Fetch the current status of a task.

        Returns:
            Task

        Raises:
            NotFoundError  if task_id doesn't exist or belongs to another developer
        """
        return self._http.get_task(task_id)

    def list(self, status=None, page=1, per_page=50):
        """
        List your submitted tasks.

        Args:
            status:    Filter by status: "pending" | "in_progress" | "completed" | "failed"
            page:      Page number, default 1
            per_page:  Results per page, max 100, default 50

        Returns:
            list[Task]
        """
        per_page = min(per_page, 100)
        return self._http.list_tasks(status=status, page=page, per_page=per_page)

    def wait_for_completion(self, task_id, poll_interval=60.0, timeout=None):
        """
        Block until a task reaches "completed" or "failed".
        Useful for short tasks (minutes). For week-long observations use the inbox instead.

        Args:
            task_id:       Task ID to watch
            poll_interval: Seconds between status checks, default 60
            timeout:       Max seconds to wait before raising TimeoutError, default None (forever)

        Returns:
            Task with final status

        Raises:
            TimeoutError  if timeout is exceeded
        """
        elapsed = 0.0
        while True:
            task = self._http.get_task(task_id)
            if not task.is_pending:
                return task
            if timeout is not None and elapsed >= timeout:
                raise TimeoutError(f"Task {task_id} did not complete within {timeout}s")
            time.sleep(poll_interval)
            elapsed += poll_interval
