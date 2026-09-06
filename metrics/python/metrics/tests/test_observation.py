"""Tests for the observation envelope builder."""

from __future__ import annotations

import uuid

from metrics.observation import (
    SCHEMA_VERSION,
    build_observation,
    new_observation_id,
    utc_now_iso,
)


def test_new_observation_id_is_uuid4():
    oid = new_observation_id()
    parsed = uuid.UUID(oid)
    assert parsed.version == 4


def test_new_observation_id_unique():
    assert new_observation_id() != new_observation_id()


def test_utc_now_iso_has_offset():
    text = utc_now_iso()
    # A timezone-aware ISO string carries a UTC offset marker.
    assert "+00:00" in text or text.endswith("Z")


def test_build_observation_defaults_wait_pile_empty():
    obs = build_observation(
        producer="p",
        entity_type="frame",
        entity_id="e1",
        context={},
        metrics={"a": 1},
    )
    assert obs["wait_pile"] == []
    assert obs["schema_version"] == SCHEMA_VERSION
    assert obs["producer"] == "p"
    assert obs["entity_type"] == "frame"
    assert obs["entity_id"] == "e1"
    assert obs["metrics"] == {"a": 1}
    assert obs["context"] == {}
    uuid.UUID(obs["observation_id"])  # does not raise


def test_build_observation_preserves_explicit_wait_pile():
    obs = build_observation(
        producer="p",
        entity_type="frame",
        entity_id="e1",
        context={},
        metrics={},
        wait_pile=["some.metric"],
    )
    assert obs["wait_pile"] == ["some.metric"]


def test_build_observation_empty_metrics_dict_preserved():
    obs = build_observation(
        producer="p",
        entity_type="frame",
        entity_id="e1",
        context={"telescope_id": "T1"},
        metrics={},
    )
    assert obs["metrics"] == {}
    assert obs["context"]["telescope_id"] == "T1"
