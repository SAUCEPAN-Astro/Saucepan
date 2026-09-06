"""Pull-based bridge worker.

Single role: claim job → object store GET → grade via compute → return
(#169). The old ``WORKER_ROLE=all`` / ``WORKER_ROLE=seed`` packet/partner/
seed-archive branches were dead (they imported ``distribution.health_gate``
and ``distribution.partners``, which never existed here) and collided with
the "R2 is a short-lived buffer, not an archive" rule — removed with the
rest of the non-R2 distribution surface. See
``docs/design/DATALAKE_R2_ONLY.md``.
"""

from __future__ import annotations

import json
import hashlib
import logging
import os
import shutil
import subprocess
import sys
import time
from pathlib import Path
from typing import Any

import http_json
from storage.factory import get_storage_backend

logger = logging.getLogger(__name__)

_local_jobs_seen: set[str] = set()


def _scratch_object_name(object_key: str) -> str:
    """Give each object key a collision-resistant private scratch name."""
    digest = hashlib.sha256(object_key.encode("utf-8")).hexdigest()[:16]
    suffix = Path(object_key).suffix.lower()
    if suffix not in {".fit", ".fits", ".fts", ".fz"}:
        suffix = ".fits"
    return f"{digest}{suffix}"


def _env_path(name: str, default: str) -> Path:
    return Path(os.environ.get(name, default))


def fetch_pending_jobs(hot_api: str, token: str) -> list[dict[str, Any]]:
    """Poll the API, whose claim endpoint removes returned jobs from pending.

    ``WORKER_JOBS_JSON`` is a one-shot local fixture fallback for development.
    """
    path = os.environ.get("WORKER_JOBS_JSON", "").strip()
    if path and Path(path).is_file():
        jobs = json.loads(Path(path).read_text(encoding="utf-8"))
        fresh = []
        for job in jobs:
            identity = json.dumps(job, sort_keys=True, default=str)
            if identity not in _local_jobs_seen:
                _local_jobs_seen.add(identity)
                fresh.append(job)
        return fresh
    url = f"{hot_api.rstrip('/')}/api/v1/worker/pending"
    try:
        out = http_json.request_json("GET", url, token=token)
        return list(out.get("jobs") or [])
    except Exception as exc:  # noqa: BLE001
        logger.warning("pending poll failed (%s) — no jobs", exc)
        return []


def grade_local(fits_path: Path, task_context: dict[str, Any]) -> dict[str, Any]:
    """Grade via compute HTTP when configured; else subprocess grade+ingest."""
    compute = os.environ.get("COMPUTE_URL", "").rstrip("/")
    if compute:
        token = os.environ.get("COMPUTE_TOKEN", "")
        return http_json.request_json(
            "POST",
            f"{compute}/v1/grade",
            {
                "staged_path": str(fits_path),
                "task_context": task_context,
                "post_ingest": True,
            },
            token=token,
        )

    # Subprocess avoids clashing with datalake's ``worker`` package name.
    compute_root = Path(__file__).resolve().parents[3] / "compute-server"
    payload = {
        "fits_path": str(fits_path),
        "task_context": task_context,
    }
    env = os.environ.copy()
    env["PYTHONPATH"] = os.pathsep.join([str(compute_root), env.get("PYTHONPATH", "")]).strip(
        os.pathsep
    )
    proc = subprocess.run(
        [
            sys.executable,
            "-c",
            (
                "import json,sys\n"
                "from worker.grading import grade_frame\n"
                "from api.ingest import post_grade_to_ingest\n"
                "req=json.load(sys.stdin)\n"
                "grade=grade_frame(req['fits_path'], req['task_context'], update_fits=False)\n"
                "tc=req['task_context']\n"
                "grade.setdefault('object_key', tc.get('object_key'))\n"
                "grade.setdefault('telescope_id', tc.get('telescope_id'))\n"
                "if grade.get('task_id') is None and tc.get('task_id') is not None:\n"
                "    grade['task_id']=tc['task_id']\n"
                "grade['ingest_response']=post_grade_to_ingest(grade)\n"
                "json.dump(grade, sys.stdout)\n"
            ),
        ],
        input=json.dumps(payload).encode("utf-8"),
        capture_output=True,
        env=env,
        check=False,
    )
    if proc.returncode != 0:
        raise RuntimeError(
            f"in-process grade failed rc={proc.returncode}: "
            f"{proc.stderr.decode('utf-8', errors='replace')[:800]}"
        )
    return json.loads(proc.stdout.decode("utf-8"))


def normalize_frame(fits_path: Path) -> Path:
    """Return a canonical SP_* FITS path for the grading/pipeline contract."""
    if not fits_path.is_file():
        # Unit tests may mock the object backend and grade function without
        # materializing bytes. The real pull path always has a file here.
        return fits_path

    compute = os.environ.get("COMPUTE_URL", "").rstrip("/")
    if compute:
        result = http_json.request_json(
            "POST",
            f"{compute}/v1/normalize",
            {"staged_path": str(fits_path), "source": "live"},
            token=os.environ.get("COMPUTE_TOKEN", ""),
        )
        output = result.get("output_path")
        if not output:
            raise RuntimeError("normalization response did not include output_path")
        return Path(output)

    output = fits_path.with_name(f"{fits_path.stem}.normalized.fits")
    compute_root = Path(__file__).resolve().parents[3] / "compute-server"
    env = os.environ.copy()
    env["PYTHONPATH"] = os.pathsep.join([str(compute_root), env.get("PYTHONPATH", "")]).strip(
        os.pathsep
    )
    # A worker scratch directory is intentionally independent from the
    # compute service's STORAGE_ROOT when grading in-process.
    env["STORAGE_ROOT"] = ""
    proc = subprocess.run(
        [
            sys.executable,
            "-c",
            (
                "import json,sys\n"
                "from normalize import normalize_fits\n"
                "result=normalize_fits(sys.argv[1], sys.argv[2], source='live')\n"
                "print(json.dumps(result.to_dict()))\n"
                "raise SystemExit(0 if result.success else 2)\n"
            ),
            str(fits_path),
            str(output),
        ],
        capture_output=True,
        env=env,
        check=False,
    )
    if proc.returncode != 0 or not output.is_file():
        raise RuntimeError(
            f"FITS normalization failed rc={proc.returncode}: "
            f"{proc.stderr.decode('utf-8', errors='replace')[:800]}"
        )
    return output


def stack_frames(frame_paths: list[Path], output_path: Path) -> dict[str, Any]:
    """Stack an explicit multi-frame product through the existing pipeline."""
    compute = os.environ.get("COMPUTE_URL", "").rstrip("/")
    if compute:
        return http_json.request_json(
            "POST",
            f"{compute}/v1/stack",
            {
                "frame_paths": [str(path) for path in frame_paths],
                "output_path": str(output_path),
                "photometric_scale": True,
                "weight_by_fwhm": True,
                "auto_crop": True,
            },
            token=os.environ.get("COMPUTE_TOKEN", ""),
        )

    compute_root = Path(__file__).resolve().parents[3] / "compute-server"
    env = os.environ.copy()
    env["PYTHONPATH"] = os.pathsep.join([str(compute_root), env.get("PYTHONPATH", "")]).strip(
        os.pathsep
    )
    proc = subprocess.run(
        [
            sys.executable,
            "-c",
            (
                "import json,sys\n"
                "from saucepan_pipeline.stacking import stack_fits_files\n"
                "summary=stack_fits_files(json.loads(sys.argv[1]), sys.argv[2])\n"
                "print(json.dumps(summary))\n"
            ),
            json.dumps([str(path) for path in frame_paths]),
            str(output_path),
        ],
        capture_output=True,
        env=env,
        check=False,
    )
    if proc.returncode != 0:
        raise RuntimeError(
            f"FITS stacking failed rc={proc.returncode}: "
            f"{proc.stderr.decode('utf-8', errors='replace')[:800]}"
        )
    lines = proc.stdout.decode("utf-8", errors="replace").splitlines()
    return json.loads(lines[-1]) if lines else {}


def publish_stack_product(store: Any, bucket: str, job: dict[str, Any], output_path: Path) -> str:
    """Put a stack in the short-lived object buffer and create an inbox row."""
    campaign_id = str(job.get("campaign_id") or "0")
    task_id = int(job["task_id"])
    object_key = f"{campaign_id}/{task_id}/stack-{task_id}.fits"
    with output_path.open("rb") as stream:
        store.put_object(
            bucket,
            object_key,
            stream,
            content_type="application/fits",
            length=output_path.stat().st_size,
        )

    hot_api = os.environ.get(
        "HOT_API_URL", os.environ.get("SAUCEPAN_API_URL", "http://127.0.0.1:8080")
    ).rstrip("/")
    token = os.environ.get("HOT_API_TOKEN", os.environ.get("WORKER_TOKEN", ""))
    http_json.request_json(
        "POST",
        f"{hot_api}/api/v1/worker/stack-product",
        {"task_id": task_id, "object_key": object_key, "bucket": bucket},
        token=token,
    )
    return object_key


def run_live_job(job: dict[str, Any]) -> dict[str, Any]:
    """Product path: pull object(s) → grade → stop (#169)."""
    task_id = job["task_id"]
    scratch = _env_path("WORKER_SCRATCH", "/tmp/saucepan-worker") / f"task_{task_id}"
    scratch.mkdir(parents=True, exist_ok=True)
    scratch.chmod(0o700)

    campaign_id = job.get("campaign_id", 0)
    telescope_id = (
        job.get("telescope_id") or os.environ.get("WORKER_TELESCOPE_ID", "").strip() or ""
    )
    object_keys = list(
        job.get("object_keys") or ([job["object_key"]] if job.get("object_key") else [])
    )
    if not object_keys:
        raise ValueError("job missing object_key(s)")

    try:
        store = get_storage_backend()
        bucket = store.bucket_for_tier()
        pulled: list[str] = []
        normalized_paths: list[Path] = []
        grades: list[dict[str, Any]] = []

        for key in object_keys:
            object_name = _scratch_object_name(key)
            dest = scratch / "raw" / object_name
            dest.parent.mkdir(parents=True, exist_ok=True)
            logger.info("pull %s/%s → %s", bucket, key, dest)
            store.download_object(bucket, key, dest)
            if dest.is_file():
                dest.chmod(0o600)
            normalized = normalize_frame(dest)
            if normalized.is_file():
                normalized.chmod(0o600)
            grade = grade_local(
                normalized,
                {
                    "campaign_id": campaign_id,
                    "task_id": task_id,
                    "object_key": key,
                    "telescope_id": telescope_id,
                    "allow_emulator": bool(job.get("allow_emulator", False)),
                    "telescope_is_emulator": bool(job.get("telescope_is_emulator", False)),
                    "idempotency_key": f"{task_id}:{key}:grade",
                    "upload_id": f"worker-{task_id}-{object_name}",
                },
            )
            if isinstance(grade, dict):
                grade.setdefault("normalized_path", str(normalized))
            grades.append(grade)
            pulled.append(key)
            normalized_paths.append(normalized)

        result: dict[str, Any] = {"task_id": task_id, "pulled": pulled, "grades": grades, "role": "process"}
        if job.get("product_mode") == "stack" and len(normalized_paths) >= 2:
            output = scratch / "products" / f"task-{task_id}-stack.fits"
            output.parent.mkdir(parents=True, exist_ok=True)
            result["stack"] = stack_frames(normalized_paths, output)
            result["stack"]["output_path"] = str(output)
            result["stack"]["object_key"] = publish_stack_product(store, bucket, job, output)
        return result
    finally:
        shutil.rmtree(scratch, ignore_errors=True)


def run_job(job: dict[str, Any]) -> dict[str, Any]:
    """Pull object(s) → grade via compute → return (#169)."""
    return run_live_job(job)


def requeue_failed_job(hot_api: str, token: str, job: dict[str, Any]) -> None:
    """Return a failed API-claimed job to the queue for a later retry."""
    if os.environ.get("WORKER_JOBS_JSON", "").strip():
        return
    try:
        http_json.request_json(
            "POST",
            f"{hot_api.rstrip('/')}/api/v1/worker/enqueue",
            job,
            token=token,
        )
    except Exception:  # noqa: BLE001
        logger.exception("could not requeue failed worker job task_id=%s", job.get("task_id"))


def run_once() -> list[dict[str, Any]]:
    hot = os.environ.get(
        "HOT_API_URL", os.environ.get("SAUCEPAN_API_URL", "http://127.0.0.1:8080")
    )
    token = os.environ.get("HOT_API_TOKEN", os.environ.get("WORKER_TOKEN", ""))
    jobs = fetch_pending_jobs(hot, token)
    if not jobs:
        return []
    concurrency = 1
    if v := os.environ.get("WORKER_CONCURRENCY", "").strip():
        try:
            concurrency = max(1, int(v))
        except ValueError:
            concurrency = 1
    if concurrency == 1 or len(jobs) == 1:
        out: list[dict[str, Any]] = []
        for job in jobs:
            try:
                out.append(run_job(job))
            except Exception:
                requeue_failed_job(hot, token, job)
                logger.exception("worker job failed task_id=%s", job.get("task_id"))
        return out
    from concurrent.futures import ThreadPoolExecutor, as_completed

    out: list[dict[str, Any]] = []
    with ThreadPoolExecutor(max_workers=concurrency) as pool:
        futs = {pool.submit(run_job, j): j for j in jobs}
        for fut in as_completed(futs):
            job = futs[fut]
            try:
                out.append(fut.result())
            except Exception:
                requeue_failed_job(hot, token, job)
                logger.exception("worker job failed task_id=%s", job.get("task_id"))
    return out


def main_loop() -> None:
    logging.basicConfig(level=logging.INFO)
    interval = float(os.environ.get("WORKER_POLL_INTERVAL", "30"))
    while True:
        try:
            results = run_once()
            for r in results:
                logger.info(
                    "job done: %s",
                    json.dumps(
                        {k: r.get(k) for k in ("task_id", "role", "pulled", "health", "r2_deleted")}
                    ),
                )
        except Exception:  # noqa: BLE001
            logger.exception("worker iteration failed")
        time.sleep(interval)


if __name__ == "__main__":
    main_loop()
