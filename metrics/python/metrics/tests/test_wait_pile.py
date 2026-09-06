"""Tests for metrics.wait_pile — loading the deferred-metric YAML."""

from __future__ import annotations

from metrics.wait_pile import load_wait_pile


def test_load_wait_pile_returns_list():
    out = load_wait_pile()
    assert isinstance(out, list)


def test_load_wait_pile_entries_are_dicts_when_present():
    out = load_wait_pile()
    for row in out:
        assert isinstance(row, dict)
