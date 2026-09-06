"""HTTP routes for FITS grading and stacking."""

from __future__ import annotations

import logging
import os
import sys
from pathlib import Path
from typing import Any

from flask import Blueprint, jsonify, request
from grading.fits_limits import FitsSizeLimitError

from . import limits
from .ingest import post_grade_to_ingest

logger = logging.getLogger(__name__)

api_bp = Blueprint("compute_api", __name__)


def _storage_root() -> Path:
    return Path(os.environ.get("STORAGE_ROOT", "/data"))


def _require_auth() -> tuple[dict[str, Any] | None, tuple[Any, int] | None]:
    """Require Bearer COMPUTE_TOKEN; local insecure mode is explicit."""
    expected = os.environ.get("COMPUTE_TOKEN", "").strip()
    if not expected:
        if os.environ.get("COMPUTE_ALLOW_INSECURE", "").strip() == "1":
            return None, None
        return None, (jsonify({"error": "COMPUTE_TOKEN required"}), 401)
    auth = request.headers.get("Authorization", "")
    if auth != f"Bearer {expected}":
        return None, (jsonify({"error": "Unauthorized"}), 401)
    return None, None


def _resolve_staged_path(staged_path: str, *, must_exist: bool = True) -> Path:
    """Resolve path under STORAGE_ROOT; reject traversal outside staging."""
    path = Path(staged_path)
    root = _storage_root()
    if not path.is_absolute():
        path = root / path
    resolved = path.resolve()
    root = root.resolve()
    if root not in resolved.parents and resolved != root:
        raise ValueError(f"staged_path outside STORAGE_ROOT: {staged_path}")
    if must_exist and not resolved.is_file():
        raise FileNotFoundError(f"FITS not found: {resolved}")
    return resolved


def _post_ingest_enabled() -> bool:
    """Server-controlled grades ingest; client JSON cannot enable this."""
    return os.environ.get("COMPUTE_POST_INGEST", "").strip() == "1"


@api_bp.route("/health", methods=["GET"])
def health():
    return jsonify({"status": "healthy", "service": "saucepan-compute"})


@api_bp.route("/v1/grade", methods=["POST"])
def grade_frame_route():
    _, err = _require_auth()
    if err:
        return err

    body = request.get_json(silent=True) or {}
    staged_path = body.get("staged_path")
    if not staged_path:
        return jsonify({"error": "staged_path required"}), 400

    raw_context = body.get("task_context") or {}
    if not isinstance(raw_context, dict):
        return jsonify({"error": "task_context must be an object"}), 400

    update_fits = bool(body.get("update_fits", False))
    # Ignore client post_ingest; fail closed unless COMPUTE_POST_INGEST=1.
    post_ingest = _post_ingest_enabled()

    try:
        fits_path = str(_resolve_staged_path(staged_path))
    except (ValueError, FileNotFoundError) as exc:
        return jsonify({"error": str(exc)}), 400

    try:
        from worker.context import apply_staged_provenance

        task_context = apply_staged_provenance(fits_path, raw_context)
    except ValueError as exc:
        # json.JSONDecodeError is a ValueError subclass, so a malformed
        # .sp_task.json lands here (as do non-dict sidecars and provenance
        # mismatches) — str(exc) carries the parser's own message.
        return jsonify({"error": str(exc)}), 400
    except OSError as exc:
        return jsonify({"error": f"invalid staged task sidecar: {exc}"}), 400

    try:
        from worker import grading as grade_worker

        grade_result = grade_worker.grade_frame(
            fits_path,
            task_context,
            update_fits=update_fits,
        )
    except FitsSizeLimitError as exc:
        return jsonify({"error": str(exc)}), 413
    except Exception as exc:
        logger.exception("grade_frame failed")
        return jsonify({"error": str(exc)}), 500

    ingest_status = None
    if post_ingest:
        try:
            if post_grade_to_ingest(grade_result):
                ingest_status = "success"
            elif os.environ.get("GRADES_INGEST_URL") or os.environ.get("FLASK_GRADES_URL"):
                ingest_status = "failed"
        except Exception:
            logger.exception("grades ingest callback failed")
            ingest_status = "failed"

    return jsonify({"grade": grade_result, "ingest_status": ingest_status})


@api_bp.route("/v1/normalize", methods=["POST"])
def normalize_frame_route():
    """Normalize a staged raw FITS file into the canonical SP_* contract."""
    _, err = _require_auth()
    if err:
        return err

    body = request.get_json(silent=True) or {}
    staged_path = body.get("staged_path")
    if not staged_path:
        return jsonify({"error": "staged_path required"}), 400
    try:
        input_path = _resolve_staged_path(staged_path)
        output_path = _resolve_staged_path(
            body.get("output_path")
            or str(input_path.with_name(f"{input_path.stem}.normalized.fits")),
            must_exist=False,
        )
    except (ValueError, FileNotFoundError) as exc:
        return jsonify({"error": str(exc)}), 400

    try:
        from normalize import normalize_fits

        result = normalize_fits(
            str(input_path),
            str(output_path),
            source=str(body.get("source") or "live"),
            base_dir=str(_storage_root()),
        )
    except Exception as exc:
        logger.exception("normalize_fits failed")
        return jsonify({"error": str(exc)}), 500
    if not result.success or not result.output_path:
        return jsonify({"error": result.error or "normalization failed", "normalization": result.to_dict()}), 422
    return jsonify({"output_path": result.output_path, "normalization": result.to_dict()})


@api_bp.route("/v1/stack", methods=["POST"])
def stack_frames_route():
    _, err = _require_auth()
    if err:
        return err

    body = request.get_json(silent=True) or {}
    frame_paths = body.get("frame_paths") or []
    output_path = body.get("output_path")
    if len(frame_paths) < 2:
        return jsonify({"error": "frame_paths must include at least 2 FITS files"}), 400
    cap = limits.max_stack_frames()
    if len(frame_paths) > cap:
        return (
            jsonify(
                {
                    "error": (
                        f"frame_paths length {len(frame_paths)} exceeds the stacking "
                        f"frame cap ({cap}); raise STACK_MEM_BUDGET_MB, lower "
                        f"STACK_TILE_PX, or set MAX_STACK_FRAMES"
                    )
                }
            ),
            400,
        )
    if not output_path:
        return jsonify({"error": "output_path required"}), 400

    try:
        resolved_inputs = [str(_resolve_staged_path(p)) for p in frame_paths]
        out = _resolve_staged_path(output_path, must_exist=False)
        out.parent.mkdir(parents=True, exist_ok=True)
    except (ValueError, FileNotFoundError) as exc:
        return jsonify({"error": str(exc)}), 400

    try:
        from grading.emulator_policy import stack_cohort_error
        from grading.fits_reader import read_sp_headers

        header_sets = [read_sp_headers(p) for p in resolved_inputs]
        if err := stack_cohort_error(header_sets):
            return jsonify({"error": err}), 400
    except Exception as exc:
        logger.exception("emulator stack cohort check failed")
        return jsonify({"error": str(exc)}), 500

    try:
        from saucepan_pipeline.stacking import stack_fits_files

        max_psf_fwhm = body.get("max_psf_fwhm")
        if max_psf_fwhm is not None:
            max_psf_fwhm = float(max_psf_fwhm)
        photometric_scale = body.get("photometric_scale", True)
        summary = stack_fits_files(
            resolved_inputs,
            str(out),
            max_psf_fwhm=max_psf_fwhm,
            photometric_scale=bool(photometric_scale),
            weight_by_fwhm=bool(body.get("weight_by_fwhm", True)),
            sigma_clip=float(body.get("sigma_clip", 3.0)),
            auto_crop=bool(body.get("auto_crop", True)),
        )
    except FitsSizeLimitError as exc:
        return jsonify({"error": str(exc)}), 413
    except Exception as exc:
        logger.exception("stack_fits_files failed")
        return jsonify({"error": str(exc)}), 500

    try:
        # Metrics package lives under top-level metrics/python/ (#426
        # consolidation, was co-located as compute-server/metrics/). Optional/
        # soft dependency: stack sidecar dispatch is best-effort, never blocks
        # the stack response.
        _metrics_pkg = Path(__file__).resolve().parents[3] / "metrics" / "python"
        if (_metrics_pkg / "metrics").is_dir() and str(_metrics_pkg) not in sys.path:
            sys.path.insert(0, str(_metrics_pkg))

        from metrics.sidecar import dispatch_stack

        dispatch_stack(
            {
                "stack_output_path": str(out),
                "_stack_summary": summary,
            },
            save_fn=None,
            sync=True,
        )
    except ImportError:
        # Was previously a silent `pass` — that let a broken metrics path
        # disable sidecar dispatch with no signal at all. Keep this
        # non-fatal (metrics are optional), but stop swallowing it silently.
        logger.warning("metrics package not importable; skipping stack sidecar dispatch")
    except Exception:
        logger.exception("stack metrics sidecar failed")

    return jsonify({"summary": summary, "output_path": str(out)})


@api_bp.route("/v1/photometry", methods=["POST"])
def photometry_route():
    _, err = _require_auth()
    if err:
        return err

    body = request.get_json(silent=True) or {}
    staged_path = body.get("staged_path")
    if not staged_path:
        return jsonify({"error": "staged_path required"}), 400

    task_context = body.get("task_context") or {}
    update_fits = bool(body.get("update_fits", False))
    run_lp = bool(body.get("run_lp", False))
    defer = bool(body.get("defer", False))

    try:
        fits_path = str(_resolve_staged_path(staged_path))
    except (ValueError, FileNotFoundError) as exc:
        return jsonify({"error": str(exc)}), 400

    def _run() -> dict[str, Any]:
        from photometry import run_lp as run_lp_step
        from photometry import run_photometry

        ctx = {**task_context, "staged_path": fits_path}
        summary = run_photometry(fits_path, ctx, update_fits=update_fits)
        if run_lp:
            summary["lp"] = run_lp_step(ctx, summary, fits_path=fits_path)
        return summary

    if defer:

        def _background() -> None:
            try:
                _run()
            except Exception:
                logger.exception("deferred photometry failed path=%s", fits_path)

        if not limits.deferred_photometry_pool().try_submit(_background):
            return (
                jsonify({"error": ("deferred photometry capacity exceeded; retry later")}),
                503,
            )
        return (
            jsonify(
                {
                    "status": "accepted",
                    "staged_path": fits_path,
                    "defer": True,
                }
            ),
            202,
        )

    try:
        summary = _run()
    except Exception as exc:
        logger.exception("photometry failed")
        return jsonify({"error": str(exc)}), 500

    return jsonify({"summary": summary, "staged_path": fits_path})
