"""Tests for metrics.bus — InProcessBus and BackgroundThreadBus."""

from __future__ import annotations

import threading
import time

from metrics.bus import BackgroundThreadBus, InProcessBus
from metrics.observation import build_observation


def _obs(entity_id: str = "e1"):
    return build_observation(
        producer="p", entity_type="frame", entity_id=entity_id, context={}, metrics={"a": 1}
    )


class _RecordingProjector:
    def __init__(self):
        self.seen = []

    def apply(self, observation):
        self.seen.append(observation)


class _RaisingProjector:
    def apply(self, observation):
        raise RuntimeError("boom")


def test_inprocess_bus_publish_no_projectors():
    bus = InProcessBus()
    obs = _obs()
    bus.publish(obs)
    assert bus.published == [obs]


def test_inprocess_bus_dispatches_to_projectors():
    bus = InProcessBus()
    proj = _RecordingProjector()
    bus.add_projector(proj)
    obs = _obs()
    bus.publish(obs)
    assert proj.seen == [obs]


def test_inprocess_bus_projector_list_constructor():
    proj = _RecordingProjector()
    bus = InProcessBus([proj])
    obs = _obs()
    bus.publish(obs)
    assert proj.seen == [obs]


def test_inprocess_bus_published_is_a_copy():
    bus = InProcessBus()
    bus.publish(_obs())
    snap = bus.published
    snap.append("mutated")
    assert len(bus.published) == 1  # internal list untouched


def test_inprocess_bus_fails_open_on_projector_exception():
    bus = InProcessBus()
    bus.add_projector(_RaisingProjector())
    good = _RecordingProjector()
    bus.add_projector(good)
    obs = _obs()
    bus.publish(obs)  # must not raise
    assert bus.published == [obs]
    assert good.seen == [obs]  # later projector still runs despite earlier failure


def test_inprocess_bus_multiple_publishes_accumulate():
    bus = InProcessBus()
    bus.publish(_obs("e1"))
    bus.publish(_obs("e2"))
    assert [o["entity_id"] for o in bus.published] == ["e1", "e2"]


def test_background_thread_bus_publishes_asynchronously():
    inner = InProcessBus()
    proj = _RecordingProjector()
    inner.add_projector(proj)
    bus = BackgroundThreadBus(inner)
    obs = _obs()
    bus.publish(obs)

    deadline = time.time() + 2.0
    while not proj.seen and time.time() < deadline:
        time.sleep(0.01)
    assert proj.seen == [obs]


def test_background_thread_bus_uses_daemon_thread(monkeypatch):
    inner = InProcessBus()
    bus = BackgroundThreadBus(inner)

    created = {}
    orig_thread = threading.Thread

    def _spy(*args, **kwargs):
        t = orig_thread(*args, **kwargs)
        created["thread"] = t
        return t

    monkeypatch.setattr(threading, "Thread", _spy)
    bus.publish(_obs())
    created["thread"].join(timeout=2.0)
    assert created["thread"].daemon is True
