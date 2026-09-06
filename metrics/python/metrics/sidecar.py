"""Sidecar entry — only surface main code may call (via datalake metrics_hook)."""

from __future__ import annotations

import logging
import os
import subprocess
import threading
import typing

from metrics.bus import InProcessBus
from metrics.emitter import run_all_producers, run_stack_producers
from metrics.observation import EntityContext, Observation
from metrics.privacy import sanitize_context
from metrics.producers import PRODUCERS
from metrics.projectors.callback import CallbackProjector

logger = logging.getLogger(__name__)

_ENABLED = os.getenv("METRICS_SIDECAR", "1").lower() not in ("0", "false", "off")


def dispatch(
    ctx: EntityContext,
    *,
    save_fn: typing.Callable[[Observation], None] | None = None,
    sync: bool = False,
) -> None:
    """
    Run live producers and publish observations.

    Args:
        ctx: Resolved entity context (upload_id, paths, catalog snapshot).
        save_fn: Projector callback (datalake injects DB writer).
        sync: If True, run inline (tests); else respects METRICS_SIDECAR_MODE.
    """
    if not _ENABLED:
        return

    def _run() -> None:
        try:
            _run_dispatch(ctx, save_fn=save_fn)
        except Exception:
            logger.exception("metrics dispatch failed upload_id=%s", ctx.get("upload_id"))

    if sync or os.getenv("METRICS_SIDECAR_MODE", "thread") == "sync":
        _run()
        return

    mode = os.getenv("METRICS_SIDECAR_MODE", "thread")
    if mode == "subprocess":
        upload_id = ctx.get("upload_id")
        if upload_id:
            import json
            import tempfile

            # Pass full context via temp file — child cannot rebuild datalake alone (#44)
            with tempfile.NamedTemporaryFile(
                mode="w", suffix=".json", delete=False, encoding="utf-8"
            ) as fh:
                json.dump(sanitize_context(ctx), fh, default=str)
                ctx_path = fh.name
            env = os.environ.copy()
            env["METRICS_CONTEXT_JSON"] = ctx_path
            env["METRICS_CONTEXT_TEMP_FILE"] = "1"
            try:
                subprocess.Popen(
                    [
                        "metrics-sidecar",
                        "emit-upload",
                        str(upload_id),
                        "--context-json",
                        ctx_path,
                    ],
                    stdout=subprocess.DEVNULL,
                    stderr=subprocess.DEVNULL,
                    env=env,
                )
            except OSError:
                try:
                    os.unlink(ctx_path)
                except OSError:
                    logger.exception("could not remove failed metrics context file")
                raise
        return

    threading.Thread(target=_run, daemon=True).start()


def dispatch_stack(
    ctx: EntityContext,
    *,
    save_fn: typing.Callable[[Observation], None] | None = None,
    sync: bool = False,
) -> None:
    """Run stack_summary producer after POST /v1/stack (fail-open)."""
    if not _ENABLED:
        return

    def _run() -> None:
        try:
            _run_stack_dispatch(ctx, save_fn=save_fn)
        except Exception:
            logger.exception("metrics stack dispatch failed")

    if sync or os.getenv("METRICS_SIDECAR_MODE", "thread") == "sync":
        _run()
        return

    threading.Thread(target=_run, daemon=True).start()


def _run_stack_dispatch(
    ctx: EntityContext,
    *,
    save_fn: typing.Callable[[Observation], None] | None,
) -> None:
    bus = InProcessBus()
    if save_fn is not None:
        bus.add_projector(CallbackProjector(save_fn))

    observations = run_stack_producers(ctx, PRODUCERS)
    for obs in observations:
        bus.publish(obs)

    logger.info("metrics stack sidecar observations=%d", len(observations))


def _run_dispatch(
    ctx: EntityContext,
    *,
    save_fn: typing.Callable[[Observation], None] | None,
) -> None:
    allowed = None
    raw = os.getenv("METRICS_PRODUCERS", "").strip()
    if raw:
        allowed = frozenset(p.strip() for p in raw.split(",") if p.strip())

    bus = InProcessBus()
    if save_fn is not None:
        bus.add_projector(CallbackProjector(save_fn))

    observations = run_all_producers(ctx, PRODUCERS, allowed=allowed)
    for obs in observations:
        bus.publish(obs)

    try:
        from metrics.insights.evaluate import evaluate
        from metrics.slo import check_slos, observations_to_metric_map

        metric_map = observations_to_metric_map(observations)
        for event in check_slos(metric_map):
            logger.warning("slo notify: %s", event)
        for ins in evaluate(observations):
            bus.publish(ins)
    except Exception:
        logger.exception("insight/slo evaluation failed upload_id=%s", ctx.get("upload_id"))

    logger.info(
        "metrics sidecar upload_id=%s observations=%d wait_pile=%d",
        ctx.get("upload_id"),
        len(observations),
        len(observations[0]["wait_pile"]) if observations else 0,
    )
