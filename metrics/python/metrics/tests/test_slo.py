"""Tests for metrics.slo — SLO breach checks, notify-only and fail-open."""

from __future__ import annotations

from metrics.observation import build_observation
from metrics.slo import (
    _breach,
    check_slos,
    load_slo_config,
    observations_to_metric_map,
)

# ---------------------------------------------------------------------------
# _breach
# ---------------------------------------------------------------------------


def test_breach_gt_true():
    assert _breach(200, 100, "gt") is True


def test_breach_gt_false():
    assert _breach(50, 100, "gt") is False


def test_breach_gte_boundary_equal_is_breach():
    assert _breach(100, 100, "gte") is True


def test_breach_lt():
    assert _breach(5, 10, "lt") is True


def test_breach_lte_boundary_equal_is_breach():
    assert _breach(10, 10, "lte") is True


def test_breach_eq_numeric():
    assert _breach(10, 10, "eq") is True


def test_breach_ne_numeric():
    assert _breach(10, 11, "ne") is True


def test_breach_unknown_op_returns_false():
    assert _breach(10, 5, "bogus") is False


def test_breach_non_numeric_value_falls_back_to_eq():
    assert _breach("offline", "offline", "eq") is True


def test_breach_non_numeric_value_falls_back_to_ne():
    assert _breach("online", "offline", "ne") is True


def test_breach_non_numeric_value_non_eq_ne_op_returns_false():
    assert _breach("offline", "online", "gt") is False


def test_breach_none_value_treated_as_non_numeric():
    assert _breach(None, 5, "gt") is False


# ---------------------------------------------------------------------------
# observations_to_metric_map
# ---------------------------------------------------------------------------


def test_observations_to_metric_map_passthrough_dict():
    flat = {"a": 1}
    assert observations_to_metric_map(flat) is flat


def test_observations_to_metric_map_flattens_observation_list():
    obs1 = build_observation(
        producer="p", entity_type="frame", entity_id="e1", context={}, metrics={"a": 1}
    )
    obs2 = build_observation(
        producer="p", entity_type="frame", entity_id="e2", context={}, metrics={"b": 2}
    )
    flat = observations_to_metric_map([obs1, obs2])
    assert flat == {"a": 1, "b": 2}


def test_observations_to_metric_map_empty_list_returns_empty_dict():
    assert observations_to_metric_map([]) == {}


def test_observations_to_metric_map_skips_non_dict_entries():
    obs = build_observation(
        producer="p", entity_type="frame", entity_id="e1", context={}, metrics={"a": 1}
    )
    flat = observations_to_metric_map([obs, "not-a-dict", 123, None])
    assert flat == {"a": 1}


def test_observations_to_metric_map_none_input():
    assert observations_to_metric_map(None) == {}


def test_observations_to_metric_map_later_observation_overrides_earlier():
    obs1 = build_observation(
        producer="p", entity_type="frame", entity_id="e1", context={}, metrics={"a": 1}
    )
    obs2 = build_observation(
        producer="p", entity_type="frame", entity_id="e2", context={}, metrics={"a": 2}
    )
    flat = observations_to_metric_map([obs1, obs2])
    assert flat["a"] == 2


# ---------------------------------------------------------------------------
# check_slos
# ---------------------------------------------------------------------------


def test_check_slos_empty_metrics_no_events():
    assert check_slos({}) == []


def test_check_slos_metric_not_present_in_flat_map_no_event():
    config = {"rules": [{"metric": "ops.missing", "threshold": 1, "op": "gt"}]}
    assert check_slos({"other": 1}, config=config) == []


def test_check_slos_rule_missing_threshold_skipped():
    config = {"rules": [{"metric": "ops.x", "op": "gt"}]}
    assert check_slos({"ops.x": 999}, config=config) == []


def test_check_slos_rule_missing_metric_field_skipped():
    config = {"rules": [{"threshold": 1, "op": "gt"}]}
    assert check_slos({"ops.x": 999}, config=config) == []


def test_check_slos_breach_produces_event_with_defaults():
    config = {"rules": [{"metric": "ops.x", "threshold": 10, "op": "gt"}]}
    events = check_slos({"ops.x": 20}, config=config)
    assert len(events) == 1
    ev = events[0]
    assert ev["event"] == "slo_breach"
    assert ev["metric"] == "ops.x"
    assert ev["value"] == 20
    assert ev["threshold"] == 10
    assert ev["action"] == "log_alert"
    assert ev["severity"] == "warn"


def test_check_slos_op_defaults_to_gt_when_absent():
    config = {"rules": [{"metric": "ops.x", "threshold": 10}]}
    events = check_slos({"ops.x": 20}, config=config)
    assert len(events) == 1


def test_check_slos_no_breach_no_event():
    config = {"rules": [{"metric": "ops.x", "threshold": 100, "op": "gt"}]}
    assert check_slos({"ops.x": 5}, config=config) == []


def test_check_slos_custom_action_and_severity_pass_through():
    config = {
        "rules": [
            {
                "metric": "ops.x",
                "threshold": 1,
                "op": "gt",
                "action": "page",
                "severity": "critical",
                "window": "5m",
            }
        ]
    }
    events = check_slos({"ops.x": 5}, config=config)
    assert events[0]["action"] == "page"
    assert events[0]["severity"] == "critical"
    assert events[0]["window"] == "5m"


def test_check_slos_multiple_rules_evaluated_independently():
    config = {
        "rules": [
            {"metric": "ops.a", "threshold": 1, "op": "gt"},
            {"metric": "ops.b", "threshold": 100, "op": "gt"},
        ]
    }
    events = check_slos({"ops.a": 5, "ops.b": 5}, config=config)
    assert len(events) == 1
    assert events[0]["metric"] == "ops.a"


def test_check_slos_fail_open_on_malformed_config():
    # cfg.get("rules") raising AttributeError (config is a list, not dict) is
    # caught by the top-level try/except -> empty list, never raises.
    events = check_slos({"ops.x": 5}, config=["not", "a", "dict"])
    assert events == []


def test_check_slos_no_rules_key_returns_empty():
    assert check_slos({"ops.x": 5}, config={}) == []


def test_check_slos_real_config_grade_duration_breach():
    # Exercises the actual slo_config.yaml on disk.
    events = check_slos({"ops.grade_duration_ms": 10_000_000})
    assert any(e["metric"] == "ops.grade_duration_ms" for e in events)


# ---------------------------------------------------------------------------
# load_slo_config
# ---------------------------------------------------------------------------


def test_load_slo_config_missing_file_returns_default(tmp_path):
    load_slo_config.cache_clear()
    try:
        cfg = load_slo_config(tmp_path / "nope.yaml")
        assert cfg == {"version": 1, "rules": []}
    finally:
        load_slo_config.cache_clear()
