"""Stage object-store blobs into local catalog paths and finalize uploads."""

from __future__ import annotations

import logging
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from catalog import Frame, Upload
from db import session_scope
from grading_hooks import on_upload_complete
from storage.backend import confined_path, safe_path_component
from storage.factory import get_storage_backend
from storage.storage_manager import LocalStorageClient

logger = logging.getLogger(__name__)

storage_client = LocalStorageClient()


class ObjectNotFoundError(RuntimeError):
    """Raised when the presigned object is unavailable in object storage."""


def object_key_for_upload(
    campaign_id: str, task_id: str | None, upload_id: str, filename: str
) -> str:
    safe_campaign = safe_path_component(campaign_id, "campaign_id")
    task_segment = safe_path_component(task_id, "task_id") if task_id else "none"
    safe_upload = safe_path_component(upload_id, "upload_id")
    safe_name = safe_path_component(Path(filename).name, "filename")
    return f"{safe_campaign}/{task_segment}/{safe_upload}_{safe_name}"


def cleanup_staged_file(path: str | Path) -> None:
    """Remove a temporary staged object after all processing hooks finish."""
    try:
        Path(path).unlink(missing_ok=True)
    except OSError:
        logger.warning("could not remove temporary staged file %s", path, exc_info=True)


def stage_from_object_store(
    bucket: str,
    object_key: str,
    campaign_id: str,
    *,
    filename: str | None = None,
    upload_id: str | None = None,
) -> tuple[str, str | None, int]:
    """Download object-store blob to STORAGE_ROOT/staging."""
    backend = get_storage_backend()
    safe_campaign = safe_path_component(campaign_id, "campaign_id")
    safe_upload = safe_path_component(upload_id, "upload_id") if upload_id else "legacy"
    name = filename or Path(object_key).name or "upload.fits"
    safe_name = safe_path_component(Path(name).name, "filename")
    staged_dir = confined_path(
        Path(storage_client.storage_root) / "staging", safe_campaign, safe_upload
    )
    staged_path = confined_path(staged_dir, safe_name)
    try:
        backend.download_object(bucket, object_key, staged_path)
        # Compute may use a shared group, but uploads must not be world-readable.
        staged_path.chmod(0o660)
        staged_dir.chmod(0o770)
        info = storage_client.get_file_info(str(staged_path))
        checksum = info.get("checksum") if info.get("success") else None
        size = info.get("size", staged_path.stat().st_size)
        return str(staged_path), checksum, size
    except Exception:
        cleanup_staged_file(staged_path)
        raise


def _rollback_catalog_finalization(upload_id: str, frame_id: str) -> None:
    """Remove a failed frame finalization so the upload can be retried."""
    try:
        with session_scope() as session:
            frame = session.get(Frame, frame_id)
            if frame is not None:
                session.delete(frame)
            upload = session.get(Upload, upload_id)
            if upload is not None and upload.status == "completed":
                upload.status = "pending"
                upload.completed_at = None
                upload.size_bytes = None
                upload.etag = None
    except Exception:
        logger.exception("could not roll back failed upload %s", upload_id)


def finalize_catalog_upload(
    upload_id: str,
    *,
    extra_metadata: dict[str, Any] | None = None,
) -> dict[str, Any]:
    """Verify object-store blob, persist frame row, run grading hook."""
    backend = get_storage_backend()
    with session_scope() as session:
        upload = session.get(Upload, upload_id)
        if upload is None:
            raise RuntimeError("Upload not found")

        bucket = upload.bucket
        object_key = upload.object_key
        campaign_id = upload.campaign_id
        filename = upload.filename

    try:
        obj_info = backend.head_object(bucket, object_key)
    except FileNotFoundError as exc:
        logger.exception("Object stat failed for %s/%s", bucket, object_key)
        raise ObjectNotFoundError("Object not found in storage") from exc
    except Exception as exc:
        logger.exception("Object storage unavailable for %s/%s", bucket, object_key)
        raise RuntimeError("Object storage unavailable") from exc

    try:
        staged_path, checksum, size = stage_from_object_store(
            bucket,
            object_key,
            campaign_id,
            filename=filename,
            upload_id=upload_id,
        )
    except FileNotFoundError as exc:
        raise ObjectNotFoundError("Object not found in storage") from exc
    except Exception as exc:
        logger.exception("Object download unavailable for %s/%s", bucket, object_key)
        raise RuntimeError("Object storage unavailable") from exc

    file_size = obj_info.get("size") or size
    frame_id: str | None = None
    try:
        with session_scope() as session:
            upload = session.get(Upload, upload_id)
            if upload is None:
                raise RuntimeError("Upload not found")
            if extra_metadata:
                merged = dict(upload.metadata_json or {})
                merged.update(extra_metadata)
                upload.metadata_json = merged
            upload.size_bytes = file_size
            upload.etag = obj_info.get("etag")
            upload.status = "completed"
            upload.completed_at = datetime.now(timezone.utc)

            frame = Frame(
                upload_id=upload.id,
                campaign_id=upload.campaign_id,
                object_key=upload.object_key,
                staged_path=staged_path,
                checksum_sha256=checksum,
                size_bytes=file_size,
            )
            session.add(frame)
            session.flush()
            frame_id = frame.id

        pipeline_status = on_upload_complete(upload_id)

        try:
            from metrics_hook import notify_upload_complete

            notify_upload_complete(upload_id)
        except Exception:
            logger.exception("metrics sidecar hook failed upload_id=%s", upload_id)
    except Exception:
        if frame_id is not None:
            _rollback_catalog_finalization(upload_id, frame_id)
        raise
    finally:
        cleanup_staged_file(staged_path)
        if frame_id is not None:
            try:
                with session_scope() as session:
                    frame = session.get(Frame, frame_id)
                    if frame is not None:
                        frame.staged_path = None
            except Exception:
                logger.exception("could not clear staged path for frame %s", frame_id)

    return {
        "success": True,
        "upload_id": upload_id,
        "frame_id": frame_id,
        "file_path": None,
        "checksum": checksum or "",
        "file_size": file_size,
        "pipeline_status": pipeline_status,
    }


# Backward-compat alias
_finalize_catalog_upload = finalize_catalog_upload
_object_key_for_upload = object_key_for_upload
_stage_from_object_store = stage_from_object_store
