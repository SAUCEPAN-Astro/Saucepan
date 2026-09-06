"""Tests for network L4 rollup (#23)."""

from __future__ import annotations

from metrics.producers import network_rollup
from metrics.projectors.network import rollup_network


def _row(tele: str, ra: float, dec: float, date_obs: str, *, zp: float | None = 22.0, **extra):
    base = {
        "telescope_id": tele,
        "ra_deg": ra,
        "dec_deg": dec,
        "date_obs": date_obs,
        "zp": zp,
        "exptime_sec": 30.0,
        "stack_eligible": True,
        "task_id": "1",
        "campaign_id": "camp",
    }
    base.update(extra)
    return base


def test_rollup_network_empty_without_catalog():
    assert rollup_network("camp") == {}
    assert rollup_network("camp", frame_catalog=[]) == {}


def test_rollup_network_two_nodes_same_target():
    catalog = [
        _row("node_a", 83.6, 22.0, "2024-01-15T22:00:00Z", zp=22.0),
        _row("node_a", 83.6, 22.0, "2024-01-15T22:05:00Z", zp=22.05),
        _row("node_b", 83.61, 22.01, "2024-01-15T22:02:00Z", zp=22.4),
        _row("node_b", 83.61, 22.01, "2024-01-15T22:08:00Z", zp=22.35),
    ]
    out = rollup_network("camp", frame_catalog=catalog)
    assert out["network.sky_coverage"] >= 1
    assert out["network.target_redundancy"] >= 2
    assert out["network.cadence_median"] is not None
    assert out["network.cadence_median"] > 0
    assert isinstance(out["network.zp_offset_per_tele"], dict)
    assert "node_a" in out["network.zp_offset_per_tele"]
    assert "node_b" in out["network.zp_offset_per_tele"]
    assert out["network.stereo_confirm_count"] >= 1
    assert out["network.campaign_exposure"] == 120.0


def test_network_rollup_producer():
    catalog = [
        _row("node_a", 10.0, 20.0, "2024-01-15T22:00:00Z"),
        _row("node_b", 10.0, 20.0, "2024-01-15T22:10:00Z"),
    ]
    produced = network_rollup.produce({"campaign_id": "camp", "frame_catalog": catalog})
    assert produced
    assert all(k.startswith("network.") for k in produced)
    assert "network.cadence_median" in produced
