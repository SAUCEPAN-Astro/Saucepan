"""Tests for node_profile and service_health producers."""

from __future__ import annotations

from datetime import datetime, timedelta, timezone

import metrics.producers.node_profile as node_profile
import metrics.producers.service_health as service_health
from metrics.observation import EntityContext

# ---------------------------------------------------------------------------
# node_profile
# ---------------------------------------------------------------------------


def test_node_profile_empty_ctx_returns_empty():
    assert node_profile.produce({}) == {}


def test_node_profile_static_fields_mapped():
    ctx: EntityContext = {
        "telescope_snapshot": {"telescope_id": "T1", "aperture_mm": 200, "mount_type": "EQ"}
    }
    out = node_profile.produce(ctx)
    assert out["node.tele_id"] == "T1"
    assert out["node.aperture_mm"] == 200
    assert out["node.mount_type"] == "EQ"


def test_node_profile_dynamic_bortle_from_fresh_cache():
    ctx: EntityContext = {
        "telescope_snapshot": {"telescope_id": "T1"},
        "node_cache": {"cached_at": _iso_ago(0), "bortle": 3},
    }
    out = node_profile.produce(ctx)
    assert out["node.bortle"] == 3


def test_node_profile_cache_fresh_within_24h():
    ctx: EntityContext = {
        "node_cache": {"cached_at": _iso_ago(hours=1), "seeing_p50": 1.8},
    }
    out = node_profile.produce(ctx)
    assert out["node.seeing_p50"] == 1.8


def test_node_profile_cache_stale_past_24h_excludes_dynamic():
    ctx: EntityContext = {
        "node_cache": {"cached_at": _iso_ago(hours=25), "seeing_p50": 1.8},
    }
    out = node_profile.produce(ctx)
    assert "node.seeing_p50" not in out


def test_node_profile_cache_boundary_just_under_24h_is_fresh():
    # A hair under the 24h cutoff avoids a real-clock race at the exact
    # boundary (test setup itself consumes a few ms before produce() runs).
    ctx: EntityContext = {
        "node_cache": {"cached_at": _iso_ago(hours=23.999), "seeing_p50": 1.8},
    }
    out = node_profile.produce(ctx)
    assert out["node.seeing_p50"] == 1.8


def test_node_profile_cache_present_without_timestamp_treated_fresh():
    ctx: EntityContext = {"node_cache": {"seeing_p50": 2.2}}
    out = node_profile.produce(ctx)
    assert out["node.seeing_p50"] == 2.2


def test_node_profile_cache_empty_dict_not_fresh():
    ctx: EntityContext = {"node_cache": {}}
    out = node_profile.produce(ctx)
    assert "node.seeing_p50" not in out


def test_node_profile_cache_malformed_timestamp_treated_fresh():
    ctx: EntityContext = {"node_cache": {"cached_at": "not-a-date", "seeing_p50": 3.1}}
    out = node_profile.produce(ctx)
    # _cache_fresh swallows ValueError and returns True.
    assert out["node.seeing_p50"] == 3.1


def test_node_profile_seeing_fallback_from_static_median():
    ctx: EntityContext = {
        "telescope_snapshot": {"median_seeing_arcsec": 2.4},
    }
    out = node_profile.produce(ctx)
    assert out["node.seeing_p50"] == 2.4
    assert out["node.median_seeing"] == 2.4


def test_node_profile_dynamic_seeing_p50_takes_priority_over_static_fallback():
    ctx: EntityContext = {
        "telescope_snapshot": {"median_seeing_arcsec": 9.9},
        "node_cache": {"seeing_p50": 1.1},
    }
    out = node_profile.produce(ctx)
    assert out["node.seeing_p50"] == 1.1


def test_node_profile_reputation_stats_reliability_and_fpr():
    ctx: EntityContext = {
        "reputation_stats": {"reliability_score": 0.88, "false_positive_rate": 0.01},
    }
    out = node_profile.produce(ctx)
    assert out["node.reliability_score"] == 0.88
    assert out["node.false_positive_rate"] == 0.01


def test_node_profile_dynamic_fpr_not_overridden_by_reputation():
    ctx: EntityContext = {
        "node_cache": {"false_positive_rate": 0.05},
        "reputation_stats": {"false_positive_rate": 0.9},
    }
    out = node_profile.produce(ctx)
    assert out["node.false_positive_rate"] == 0.05


def test_node_profile_node_id_falls_back_to_telescope_id():
    ctx: EntityContext = {"telescope_id": "T-ONLY"}
    out = node_profile.produce(ctx)
    assert out["node.node_id"] == "T-ONLY"
    assert out["node.tele_id"] == "T-ONLY"


def test_node_profile_clock_source_from_snapshot_when_ctx_absent():
    ctx: EntityContext = {"telescope_snapshot": {"clock_source": "GPS"}}
    out = node_profile.produce(ctx)
    assert out["node.clock_source"] == "GPS"


def _iso_ago(hours: float) -> str:
    ts = datetime.now(timezone.utc) - timedelta(hours=hours)
    return ts.isoformat()


# ---------------------------------------------------------------------------
# service_health
# ---------------------------------------------------------------------------


def test_service_health_empty_ctx_returns_empty():
    assert service_health.produce({}) == {}


def test_service_health_empty_timing_dict_no_status_set():
    out = service_health.produce({"_timing": {}})
    assert "ops.svc_status" not in out


def test_service_health_status_ok_when_timing_has_data_no_errors():
    out = service_health.produce({"_timing": {"grade_duration_ms": 500}})
    assert out["ops.svc_status"] == "ok"


def test_service_health_status_degraded_with_single_error_is_generic():
    out = service_health.produce({"_timing": {"errors": ["disk full"]}})
    assert out["ops.svc_status"] == "degraded"
    assert out["ops.compute_error"] == "compute_error"


def test_service_health_status_degraded_with_multiple_errors_is_generic():
    out = service_health.produce({"_timing": {"errors": ["e1", "e2"]}})
    assert out["ops.compute_error"] == "compute_error"


def test_service_health_breach_flags_only_set_when_true():
    out = service_health.produce(
        {"_timing": {"grade_duration_p95_breach": True, "orch_latency_p99_breach": False}}
    )
    assert out["ops.grade_duration_p95_breach"] is True
    assert "ops.orch_latency_p99_breach" not in out


def test_service_health_ops_context_fallback_when_timing_absent():
    out = service_health.produce({"_ops": {"task_failure_count": 3}})
    assert out["ops.task_failure_count"] == 3


def test_service_health_timing_ops_context_takes_priority_over_ops_dict():
    out = service_health.produce(
        {
            "_timing": {"task_failure_count": 1},
            "_ops": {"task_failure_count": 99},
        }
    )
    assert out["ops.task_failure_count"] == 1


def test_service_health_campaign_id_from_ops_when_absent_at_top_level():
    out = service_health.produce({"_ops": {"campaign_id": "c-42"}})
    assert out["ops.campaign_id"] == "c-42"


def test_service_health_campaign_id_top_level_wins_over_ops():
    out = service_health.produce({"campaign_id": "c-top", "_ops": {"campaign_id": "c-ops"}})
    assert out["ops.campaign_id"] == "c-top"


def test_service_health_non_dict_timing_ignored():
    out = service_health.produce({"_timing": "not-a-dict"})
    assert out == {}


def test_service_health_non_dict_ops_ignored():
    out = service_health.produce({"_ops": "not-a-dict"})
    assert out == {}
