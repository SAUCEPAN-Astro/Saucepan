"""Tests for metrics.emitter — producer execution and observation assembly."""

from __future__ import annotations

from metrics.emitter import run_all_producers, run_producer, run_stack_producers
from metrics.observation import EntityContext


def _ok_producer(ctx):
    return {"m.value": 1}


def _empty_producer(ctx):
    return {}


def _none_producer(ctx):
    return None


def _raising_producer(ctx):
    raise RuntimeError("producer exploded")


def test_run_producer_happy_path():
    ctx: EntityContext = {"upload_id": "u1"}
    obs = run_producer("ok", _ok_producer, ctx)
    assert obs is not None
    assert obs["producer"] == "ok"
    assert obs["entity_type"] == "frame"
    assert obs["entity_id"] == "u1"
    assert obs["metrics"] == {"m.value": 1}


def test_run_producer_redacts_identity_and_local_path_fields():
    def producer(_ctx):
        return {
            "m.value": 1,
            "frame.observer": "private",
            "ops.staging_path": "/private/frame.fits",
            "ops.compute_error": "private backend detail",
            "task.paths": ["/private/frame.fits"],
            "frame.target_name": "M31",
            "task.name": "M31 survey",
        }

    obs = run_producer(
        "private",
        producer,
        {
            "upload_id": "u1",
            "observer_display_name": "private",
            "user": "private",
            "identity": "private",
            "name": "private",
            "fullName": "private",
            "personName": "private",
        },
    )
    assert obs["metrics"] == {
        "m.value": 1,
        "ops.compute_error": "error",
        "frame.target_name": "M31",
        "task.name": "M31 survey",
    }
    assert obs["context"] == {"upload_id": "u1"}


def test_run_producer_returns_none_on_empty_metrics():
    ctx: EntityContext = {"upload_id": "u1"}
    assert run_producer("empty", _empty_producer, ctx) is None


def test_run_producer_returns_none_on_none_metrics():
    ctx: EntityContext = {"upload_id": "u1"}
    assert run_producer("none", _none_producer, ctx) is None


def test_run_producer_fails_open_on_exception():
    ctx: EntityContext = {"upload_id": "u1"}
    assert run_producer("raises", _raising_producer, ctx) is None


def test_run_producer_entity_id_fallback_chain():
    # No entity_id_key match ("upload_id" absent) falls back to frame_id.
    ctx: EntityContext = {"frame_id": "f1"}
    obs = run_producer("ok", _ok_producer, ctx)
    assert obs["entity_id"] == "f1"


def test_run_producer_entity_id_fallback_to_stack_output_path():
    ctx: EntityContext = {"stack_output_path": "/tmp/stack.fits"}
    obs = run_producer("ok", _ok_producer, ctx)
    assert len(obs["entity_id"]) == 16
    assert obs["entity_id"] != "/tmp/stack.fits"


def test_run_producer_entity_id_fallback_to_unknown():
    ctx: EntityContext = {}
    obs = run_producer("ok", _ok_producer, ctx)
    assert obs["entity_id"] == "unknown"


def test_run_producer_custom_entity_type_and_key():
    ctx: EntityContext = {"night_id": "T1_2026-01-01"}
    obs = run_producer("ok", _ok_producer, ctx, entity_type="session", entity_id_key="night_id")
    assert obs["entity_type"] == "session"
    assert obs["entity_id"] == "T1_2026-01-01"


def test_run_all_producers_skips_non_live_names():
    ctx: EntityContext = {"upload_id": "u1"}
    producers = {"upload_context": lambda c: {"frame.upload_id": c.get("upload_id")}}
    obs = run_all_producers(ctx, producers, allowed=frozenset())
    assert obs == []


def test_run_all_producers_empty_producers_dict():
    ctx: EntityContext = {"upload_id": "u1"}
    assert run_all_producers(ctx, {}) == []


def test_run_all_producers_skips_producer_returning_empty():
    ctx: EntityContext = {"upload_id": "u1"}
    producers = {"upload_context": _empty_producer}
    obs = run_all_producers(ctx, producers, allowed=frozenset({"upload_context"}))
    assert obs == []


def test_run_stack_producers_missing_fn_returns_empty():
    ctx: EntityContext = {"stack_output_path": "/tmp/x.fits"}
    assert run_stack_producers(ctx, {}) == []


def test_run_stack_producers_produces_stack_entity():
    ctx: EntityContext = {
        "stack_output_path": "/tmp/x.fits",
        "_stack_summary": {"n_frames_used": 3},
    }
    producers = {
        "stack_summary": lambda c: {"stack.n_frames": c["_stack_summary"]["n_frames_used"]}
    }
    obs = run_stack_producers(ctx, producers)
    assert len(obs) == 1
    assert obs[0]["entity_type"] == "stack"
    assert len(obs[0]["entity_id"]) == 16
    assert obs[0]["entity_id"] != "/tmp/x.fits"
    assert obs[0]["metrics"] == {"stack.n_frames": 3}


def test_run_stack_producers_empty_metrics_yields_no_observation():
    ctx: EntityContext = {"stack_output_path": "/tmp/x.fits"}
    producers = {"stack_summary": _empty_producer}
    assert run_stack_producers(ctx, producers) == []
