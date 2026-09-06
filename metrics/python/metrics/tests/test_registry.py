"""Tests for metrics.registry — live/wait registry loading and partitioning."""

from __future__ import annotations

import pathlib

import pytest
import yaml
from metrics.registry import (
    live_metric_ids,
    load_registry,
    producers_for_status,
    wait_metrics,
)


def _write_registry(tmp_path: pathlib.Path, rows: list[dict]) -> pathlib.Path:
    path = tmp_path / "registry.yaml"
    path.write_text(yaml.safe_dump({"metrics": rows}), encoding="utf-8")
    return path


def test_load_registry_partitions_live_and_wait(tmp_path):
    path = _write_registry(
        tmp_path,
        [
            {"id": "a.live", "status": "live", "layer": "L1", "producer": "prod_a"},
            {"id": "b.wait", "status": "wait", "layer": "L2", "wait_reason": "needs Q&A"},
            {"id": "c.live", "status": "live", "layer": "L1", "producer": "prod_a"},
        ],
    )
    reg = load_registry(path)
    assert set(reg.keys()) == {"a.live", "b.wait", "c.live"}
    assert reg["b.wait"].wait_reason == "needs Q&A"
    assert reg["a.live"].producer == "prod_a"

    live_ids = live_metric_ids(reg)
    assert live_ids == frozenset({"a.live", "c.live"})

    waiting = wait_metrics(reg)
    assert [s.id for s in waiting] == ["b.wait"]


def test_load_registry_empty_metrics_key(tmp_path):
    path = tmp_path / "registry.yaml"
    path.write_text(yaml.safe_dump({"metrics": []}), encoding="utf-8")
    reg = load_registry(path)
    assert reg == {}


def test_load_registry_missing_metrics_key_defaults_empty(tmp_path):
    path = tmp_path / "registry.yaml"
    path.write_text(yaml.safe_dump({"version": 1}), encoding="utf-8")
    reg = load_registry(path)
    assert reg == {}


def test_load_registry_missing_layer_defaults_blank(tmp_path):
    path = _write_registry(tmp_path, [{"id": "x", "status": "live"}])
    reg = load_registry(path)
    assert reg["x"].layer == ""
    assert reg["x"].producer is None
    assert reg["x"].wait_reason is None


def test_producers_for_status_dedupes_and_filters(tmp_path):
    path = _write_registry(
        tmp_path,
        [
            {"id": "a", "status": "live", "producer": "prod_a"},
            {"id": "b", "status": "live", "producer": "prod_a"},
            {"id": "c", "status": "live", "producer": "prod_b"},
            {"id": "d", "status": "wait"},  # no producer
            {"id": "e", "status": "wait", "producer": "prod_c"},
        ],
    )
    reg = load_registry(path)
    live_producers = producers_for_status("live", reg)
    assert live_producers == frozenset({"prod_a", "prod_b"})

    wait_producers = producers_for_status("wait", reg)
    assert wait_producers == frozenset({"prod_c"})


def test_live_metric_ids_uses_default_registry_when_none_passed():
    # Exercises the real repo registry.yaml — sanity check on shape, not count.
    ids = live_metric_ids()
    assert isinstance(ids, frozenset)
    assert len(ids) > 0
    for mid in list(ids)[:3]:
        assert isinstance(mid, str)


def test_load_registry_raises_for_missing_file(tmp_path):
    missing = tmp_path / "does_not_exist.yaml"
    with pytest.raises(FileNotFoundError):
        load_registry(missing)
