"""Producer: deploy / pipeline version stamps on every observation."""

from __future__ import annotations

import os

from metrics.observation import EntityContext


def _env(key: str, fallback: str = "") -> str:
    return os.getenv(key, fallback).strip() or fallback


def produce(ctx: EntityContext) -> dict[str, object]:
    del ctx  # governance is env-scoped, not entity-scoped
    norm_ver = _env("GOV_PIPELINE_NORM_VER")
    if not norm_ver:
        try:
            from normalize.normalize import __version__ as norm_ver  # type: ignore[attr-defined]
        except ImportError:
            norm_ver = ""

    grader_ver = _env("GOV_GRADER_VER")
    if not grader_ver:
        try:
            import grading

            grader_ver = getattr(grading, "__version__", "")
        except ImportError:
            grader_ver = ""

    out: dict[str, object] = {}
    fields = {
        "gov.pipeline_norm_ver": norm_ver,
        "gov.pipeline_module_ver": _env("GOV_PIPELINE_MODULE_VER", "stacking 1.0.0"),
        "gov.grader_ver": grader_ver,
        "gov.client_ver": _env("GOV_CLIENT_VER"),
        "gov.compute_image_digest": _env("GOV_COMPUTE_IMAGE_DIGEST"),
        "gov.schema_compliance_rate": _env("GOV_SCHEMA_COMPLIANCE_RATE"),
        "gov.test_coverage_pct": _env("GOV_TEST_COVERAGE_PCT"),
        "gov.repro_bundle_id": _env("GOV_REPRO_BUNDLE_ID"),
        "gov.deploy_env": _env("GOV_DEPLOY_ENV", _env("DEPLOY_ENV", "dev")),
    }
    for metric_id, val in fields.items():
        if val:
            out[metric_id] = val
    return out
