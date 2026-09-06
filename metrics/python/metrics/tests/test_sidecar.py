"""Sidecar and registry tests."""

from __future__ import annotations

import pathlib
import sys

ROOT = pathlib.Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT.parent))

from metrics.emitter import run_all_producers, run_producer  # noqa: E402
from metrics.observation import EntityContext  # noqa: E402
from metrics.producers import PRODUCERS  # noqa: E402
from metrics.registry import load_registry, wait_metrics  # noqa: E402
from metrics.sidecar import dispatch  # noqa: E402


def test_registry_live_and_wait_partition():
    reg = load_registry()
    assert len(reg) >= 300
    live = [s for s in reg.values() if s.status == "live"]
    wait = wait_metrics()
    assert live
    # Wait pile may be empty after #370; partition must still sum.
    assert len(live) + len(wait) == len(reg)


def test_dispatch_fail_open_no_save():
    ctx: EntityContext = {
        "upload_id": "test-upload",
        "frame_id": "test-frame",
        "telescope_id": "TELE_TEST",
        "campaign_id": "camp",
    }
    dispatch(ctx, save_fn=None, sync=True)  # must not raise


def test_upload_context_producer():
    ctx: EntityContext = {
        "upload_id": "u1",
        "frame_id": "f1",
        "telescope_id": "T1",
        "campaign_id": "c1",
        "clock_source": "NTP",
        "detector_temp_c": -15.5,
    }
    obs = run_all_producers(ctx, PRODUCERS, allowed=frozenset({"upload_context"}))
    assert len(obs) == 1
    assert obs[0]["metrics"]["frame.upload_id"] == "u1"
    assert obs[0]["metrics"]["frame.timesys"] == "NTP"
    assert obs[0]["metrics"]["frame.detector_temp"] == -15.5


def test_upload_context_omits_observer_display_name():
    ctx: EntityContext = {
        "upload_id": "u1",
        "observer_display_name": "test observer",
    }
    obs = run_all_producers(ctx, PRODUCERS, allowed=frozenset({"upload_context"}))
    assert "frame.observer" not in obs[0]["metrics"]


def test_upload_context_omits_observer_user_id():
    ctx: EntityContext = {
        "upload_id": "u1",
        "user_id": "usr-42",
    }
    obs = run_all_producers(ctx, PRODUCERS, allowed=frozenset({"upload_context"}))
    assert "frame.observer" not in obs[0]["metrics"]


def test_stack_summary_producer():
    ctx: EntityContext = {
        "stack_output_path": "/tmp/stacked.fits",
        "_stack_summary": {
            "n_frames_used": 5,
            "n_frames_rejected": 1,
            "stack_snr": 42.0,
            "snr_gain": 2.1,
            "efficiency": 0.9,
            "theoretical_max": 2.24,
            "provenance": [
                {"telescope_id": "T1", "weight_pct": 60.0, "weight": 1.2, "rejected": False},
                {"telescope_id": "T2", "weight_pct": 40.0, "weight": 0.8, "rejected": True},
            ],
        },
    }
    obs = run_all_producers(ctx, PRODUCERS, allowed=frozenset({"stack_summary"}))
    assert len(obs) == 1
    m = obs[0]["metrics"]
    assert m["stack.n_frames"] == 5
    assert m["stack.n_reject"] == 1
    assert m["stack.snr"] == 42.0
    assert m["stack.provenance_tele_id"] == "T1"
    assert m["stack.provenance_rejected"] == 1


def test_governance_producer():
    ctx: EntityContext = {"upload_id": "u1"}
    obs = run_all_producers(ctx, PRODUCERS, allowed=frozenset({"governance"}))
    assert len(obs) == 1
    assert "gov.pipeline_norm_ver" in obs[0]["metrics"]
    assert obs[0]["metrics"]["gov.deploy_env"] == "dev"


def test_upload_context_producer_wait_pile():
    ctx: EntityContext = {
        "upload_id": "u1",
        "frame_id": "f1",
        "telescope_id": "T1",
        "campaign_id": "c1",
    }
    obs = run_all_producers(ctx, PRODUCERS, allowed=frozenset({"upload_context"}))
    assert len(obs) == 1
    assert obs[0]["metrics"]["frame.upload_id"] == "u1"
    assert obs[0]["wait_pile"] == [s.id for s in wait_metrics()]


def test_observation_includes_full_wait_pile():
    ctx: EntityContext = {"upload_id": "u1"}
    obs = run_all_producers(ctx, PRODUCERS, allowed=frozenset({"upload_context"}))
    wait_ids = {s.id for s in wait_metrics()}
    assert set(obs[0]["wait_pile"]) == wait_ids


def test_session_rollup_producer():
    from metrics.producers.session_rollup import produce

    ctx: EntityContext = {
        "telescope_id": "T1",
        "night_id": "T1_2026-07-01",
        "_session_rollup": {
            "session.night_id": "T1_2026-07-01",
            "session.frames": 12,
            "session.phot_class": "PHOT",
        },
    }
    obs = run_producer(
        "session_rollup",
        produce,
        ctx,
        entity_type="session",
        entity_id_key="night_id",
    )
    assert obs is not None
    assert obs["metrics"]["session.frames"] == 12


def test_node_profile_producer():
    from metrics.producers.node_profile import produce

    ctx: EntityContext = {
        "node_id": "NODE_1",
        "clock_source": "NTP",
        "telescope_snapshot": {
            "telescope_id": "T1",
            "aperture_mm": 200,
            "site_latitude": 45.0,
        },
        "node_cache": {"bortle": 4, "seeing_p50": 2.1},
    }
    obs = run_producer(
        "node_profile", produce, ctx, entity_type="node", entity_id_key="telescope_id"
    )
    assert obs is not None
    m = obs["metrics"]
    assert m["node.tele_id"] == "T1"
    assert m["node.aperture_mm"] == 200
    assert m["node.bortle"] == 4
    assert m["node.seeing_p50"] == 2.1


def test_service_health_producer():
    from metrics.producers.service_health import produce

    ctx: EntityContext = {
        "_timing": {"grade_duration_ms": 1200, "stack_duration_ms": 4500},
    }
    obs = run_producer("service_health", produce, ctx)
    assert obs is not None
    m = obs["metrics"]
    assert m["ops.grade_duration_ms"] == 1200
    assert m["ops.svc_status"] == "ok"


def test_rollup_night_phot_class():
    from metrics.projectors.session import rollup_night

    metrics = rollup_night(
        "T1",
        "T1_2026-07-01",
        frames=[
            {"zp": 22.0, "exptime_sec": 60},
            {"zp": 22.0, "exptime_sec": 60},
        ],
    )
    assert metrics["session.phot_class"] == "PHOT"
    assert metrics["session.frames"] == 2


def test_insight_evaluate_session_nonphot_and_zp_drift():
    """SESSION_NONPHOT + ZP_DRIFT enabled once session rollups are live (#29)."""
    from metrics.insights.evaluate import evaluate, load_insight_rules
    from metrics.observation import build_observation

    load_insight_rules.cache_clear()
    obs = build_observation(
        producer="session_rollup",
        entity_type="session",
        entity_id="T1_2026-07-01",
        context={"telescope_id": "T1", "night_id": "T1_2026-07-01"},
        metrics={"session.phot_class": "NONPHOT", "session.zp_drift": 0.2},
    )
    insights = evaluate(obs)
    types = {i["metrics"]["insight.event_type"] for i in insights}
    assert "SESSION_NONPHOT" in types
    assert "ZP_DRIFT" in types
    for ins in insights:
        assert ins["metrics"].get("insight.severity") in ("warn", "critical")


def test_slo_check_fail_open():
    from metrics.slo import check_slos

    events = check_slos({"ops.grade_duration_ms": 999999})
    assert events
    assert events[0]["action"] == "log_alert"


def test_slo_check_accepts_observation_list():
    """#368 — sidecar passes Observation list; must not silently no-op."""
    from metrics.observation import build_observation
    from metrics.slo import check_slos

    obs = build_observation(
        producer="service_health",
        entity_type="ops",
        entity_id="u1",
        context={},
        metrics={"ops.grade_duration_ms": 999999},
    )
    events = check_slos([obs])
    assert events
    assert events[0]["metric"] == "ops.grade_duration_ms"


def test_service_health_ops_context():
    from metrics.producers.service_health import produce

    ctx: EntityContext = {
        "campaign_id": "c9",
        "_timing": {
            "grade_duration_ms": 120,
            "task_failure_count": 2,
            "handoff_requested": True,
        },
        "_ops": {"prometheus_export": True, "scope_heartbeat": 1},
    }
    out = produce(ctx)
    assert out["ops.campaign_id"] == "c9"
    assert out["ops.grade_duration_ms"] == 120
    assert out["ops.task_failure_count"] == 2
    assert out["ops.handoff_requested"] is True
    assert out["ops.prometheus_export"] is True
    assert out["ops.scope_heartbeat"] == 1
