"""Tests for metrics.insights.evaluate — rule parsing and breach evaluation."""

from __future__ import annotations

from metrics.insights.evaluate import (
    _match_when,
    _parse_value,
    evaluate,
    load_insight_rules,
)
from metrics.observation import build_observation


def _obs(metrics: dict, context: dict | None = None):
    return build_observation(
        producer="test",
        entity_type="frame",
        entity_id="e1",
        context=context or {},
        metrics=metrics,
    )


# ---------------------------------------------------------------------------
# _parse_value
# ---------------------------------------------------------------------------


def test_parse_value_quoted_single_quotes():
    assert _parse_value("'offline'") == "offline"


def test_parse_value_quoted_double_quotes():
    assert _parse_value('"offline"') == "offline"


def test_parse_value_integer():
    assert _parse_value("30000") == 30000
    assert isinstance(_parse_value("30000"), int)


def test_parse_value_float():
    assert _parse_value("0.05") == 0.05
    assert isinstance(_parse_value("0.05"), float)


def test_parse_value_unparseable_text_returned_as_is():
    assert _parse_value("NONPHOT") == "NONPHOT"


def test_parse_value_strips_whitespace():
    assert _parse_value("  42  ") == 42


# ---------------------------------------------------------------------------
# _match_when
# ---------------------------------------------------------------------------


def test_match_when_numeric_gt_true():
    assert _match_when("ops.grade_duration_ms > 100", {"ops.grade_duration_ms": 200}) is True


def test_match_when_numeric_gt_false():
    assert _match_when("ops.grade_duration_ms > 100", {"ops.grade_duration_ms": 50}) is False


def test_match_when_string_equality():
    assert _match_when("session.phot_class == 'NONPHOT'", {"session.phot_class": "NONPHOT"}) is True


def test_match_when_missing_metric_is_false():
    assert _match_when("ops.grade_duration_ms > 100", {}) is False


def test_match_when_malformed_expression_is_false():
    assert _match_when("not a valid expression!!", {"x": 1}) is False


def test_match_when_type_mismatch_swallowed_as_false():
    # Comparing an int metric against a string literal raises TypeError internally.
    assert _match_when("ops.grade_duration_ms > 'nope'", {"ops.grade_duration_ms": 5}) is False


def test_match_when_underscore_alias_resolution():
    # _resolve_metric falls back to dot->underscore alias.
    assert _match_when("ops.grade_duration_ms > 1", {"ops_grade_duration_ms": 5}) is True


def test_match_when_not_equal_operator():
    assert _match_when("ops.svc_status != 'ok'", {"ops.svc_status": "degraded"}) is True


# ---------------------------------------------------------------------------
# evaluate
# ---------------------------------------------------------------------------


def test_evaluate_no_matching_rules_returns_empty():
    obs = _obs({"ops.grade_duration_ms": 1})
    rules = [{"id": "r1", "when": "ops.grade_duration_ms > 999999", "emit": "SLOW"}]
    assert evaluate(obs, rules=rules) == []


def test_evaluate_disabled_rule_skipped():
    obs = _obs({"ops.grade_duration_ms": 999999})
    rules = [{"id": "r1", "enabled": False, "when": "ops.grade_duration_ms > 1", "emit": "SLOW"}]
    assert evaluate(obs, rules=rules) == []


def test_evaluate_rule_without_when_is_skipped():
    obs = _obs({"ops.grade_duration_ms": 999999})
    rules = [{"id": "r1", "emit": "SLOW"}]
    assert evaluate(obs, rules=rules) == []


def test_evaluate_matching_rule_produces_insight_observation():
    obs = _obs({"ops.grade_duration_ms": 999999}, context={"telescope_id": "T1"})
    rules = [
        {
            "id": "grade_slow",
            "when": "ops.grade_duration_ms > 30000",
            "emit": "GRADE_SLOW",
            "severity": "warn",
            "attach": ["telescope_id"],
        }
    ]
    insights = evaluate(obs, rules=rules)
    assert len(insights) == 1
    ins = insights[0]
    assert ins["producer"] == "insight_evaluator"
    assert ins["entity_type"] == "insight"
    assert ins["metrics"]["insight.event_type"] == "GRADE_SLOW"
    assert ins["metrics"]["insight.severity"] == "warn"
    assert ins["metrics"]["insight.evidence"]["telescope_id"] == "T1"


def test_evaluate_attach_key_missing_from_context_omitted():
    obs = _obs({"ops.grade_duration_ms": 999999})
    rules = [
        {
            "id": "r1",
            "when": "ops.grade_duration_ms > 1",
            "emit": "SLOW",
            "attach": ["telescope_id"],
        }
    ]
    insights = evaluate(obs, rules=rules)
    assert "telescope_id" not in insights[0]["metrics"]["insight.evidence"]


def test_evaluate_defaults_severity_to_info():
    obs = _obs({"ops.grade_duration_ms": 999999})
    rules = [{"id": "r1", "when": "ops.grade_duration_ms > 1", "emit": "SLOW"}]
    insights = evaluate(obs, rules=rules)
    assert insights[0]["metrics"]["insight.severity"] == "info"


def test_evaluate_event_type_falls_back_to_rule_id_when_no_emit():
    obs = _obs({"ops.grade_duration_ms": 999999})
    rules = [{"id": "custom_rule", "when": "ops.grade_duration_ms > 1"}]
    insights = evaluate(obs, rules=rules)
    assert insights[0]["metrics"]["insight.event_type"] == "custom_rule"


def test_evaluate_accepts_single_observation_not_list():
    obs = _obs({"ops.grade_duration_ms": 999999})
    rules = [{"id": "r1", "when": "ops.grade_duration_ms > 1", "emit": "SLOW"}]
    insights = evaluate(obs, rules=rules)  # obs is not wrapped in a list
    assert len(insights) == 1


def test_evaluate_accepts_list_of_observations():
    obs1 = _obs({"ops.grade_duration_ms": 999999})
    obs2 = _obs({"ops.grade_duration_ms": 1})
    rules = [{"id": "r1", "when": "ops.grade_duration_ms > 1", "emit": "SLOW"}]
    insights = evaluate([obs1, obs2], rules=rules)
    assert len(insights) == 1


def test_evaluate_wait_pile_propagated_from_source_observation():
    obs = _obs({"ops.grade_duration_ms": 999999})
    obs["wait_pile"] = ["some.deferred.metric"]
    rules = [{"id": "r1", "when": "ops.grade_duration_ms > 1", "emit": "SLOW"}]
    insights = evaluate(obs, rules=rules)
    assert insights[0]["wait_pile"] == ["some.deferred.metric"]


def test_evaluate_multiple_matching_rules_produce_multiple_insights():
    obs = _obs({"ops.grade_duration_ms": 999999, "session.phot_class": "NONPHOT"})
    rules = [
        {"id": "r1", "when": "ops.grade_duration_ms > 1", "emit": "SLOW"},
        {"id": "r2", "when": "session.phot_class == 'NONPHOT'", "emit": "NONPHOT_EV"},
    ]
    insights = evaluate(obs, rules=rules)
    types = {i["metrics"]["insight.event_type"] for i in insights}
    assert types == {"SLOW", "NONPHOT_EV"}


def test_load_insight_rules_missing_file_returns_empty(tmp_path):
    missing = tmp_path / "does_not_exist.yaml"
    load_insight_rules.cache_clear()
    try:
        assert load_insight_rules(missing) == []
    finally:
        load_insight_rules.cache_clear()


def test_load_insight_rules_real_file_has_rules():
    load_insight_rules.cache_clear()
    rules = load_insight_rules()
    assert isinstance(rules, list)
    assert len(rules) > 0
    assert all("id" in r for r in rules)
