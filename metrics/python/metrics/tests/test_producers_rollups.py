"""Tests for session_rollup and network_rollup producers (thin wrappers over projectors)."""

from __future__ import annotations

import metrics.producers.network_rollup as network_rollup
import metrics.producers.session_rollup as session_rollup
from metrics.observation import EntityContext

# ---------------------------------------------------------------------------
# session_rollup producer
# ---------------------------------------------------------------------------


def test_session_rollup_no_rollup_key_returns_empty():
    assert session_rollup.produce({}) == {}


def test_session_rollup_non_dict_rollup_returns_empty():
    assert session_rollup.produce({"_session_rollup": "nope"}) == {}


def test_session_rollup_filters_to_session_prefixed_keys():
    ctx: EntityContext = {
        "_session_rollup": {
            "session.frames": 10,
            "not.session.key": 1,
            "random": "junk",
        }
    }
    out = session_rollup.produce(ctx)
    assert out == {"session.frames": 10}


def test_session_rollup_allows_frame_extinction_coeff_passthrough():
    ctx: EntityContext = {"_session_rollup": {"session.frames": 5, "frame.extinction_coeff": 0.15}}
    out = session_rollup.produce(ctx)
    assert out["frame.extinction_coeff"] == 0.15


def test_session_rollup_drops_none_values():
    ctx: EntityContext = {"_session_rollup": {"session.frames": None, "session.zp_median": 22.0}}
    out = session_rollup.produce(ctx)
    assert out == {"session.zp_median": 22.0}


def test_session_rollup_empty_rollup_dict_returns_empty():
    assert session_rollup.produce({"_session_rollup": {}}) == {}


# ---------------------------------------------------------------------------
# network_rollup producer
# ---------------------------------------------------------------------------


def test_network_rollup_no_campaign_id_returns_empty():
    assert network_rollup.produce({}) == {}


def test_network_rollup_campaign_id_without_catalog_returns_empty():
    assert network_rollup.produce({"campaign_id": "c1"}) == {}


def test_network_rollup_catalog_not_a_list_returns_empty():
    assert network_rollup.produce({"campaign_id": "c1", "frame_catalog": "nope"}) == {}


def test_network_rollup_precomputed_rollup_filters_network_prefixed_only():
    ctx: EntityContext = {
        "_network_rollup": {
            "network.sky_coverage": 3.0,
            "_campaign_id": "c1",  # internal key, must be dropped
            "network.cadence_median": None,  # None values dropped
        }
    }
    out = network_rollup.produce(ctx)
    assert out == {"network.sky_coverage": 3.0}


def test_network_rollup_precomputed_empty_dict_returns_empty():
    assert network_rollup.produce({"_network_rollup": {}}) == {}


def test_network_rollup_precomputed_non_dict_falls_through_to_catalog_path():
    # _network_rollup explicitly None triggers the campaign_id/catalog path,
    # which is empty here -> {}.
    ctx: EntityContext = {"_network_rollup": None, "campaign_id": "c1"}
    out = network_rollup.produce(ctx)
    assert out == {}


def test_network_rollup_computes_from_frame_catalog():
    catalog = [
        {
            "telescope_id": "node_a",
            "ra_deg": 10.0,
            "dec_deg": 20.0,
            "date_obs": "2024-01-15T22:00:00Z",
            "zp": 22.0,
            "exptime_sec": 30.0,
        },
        {
            "telescope_id": "node_b",
            "ra_deg": 10.0,
            "dec_deg": 20.0,
            "date_obs": "2024-01-15T22:05:00Z",
            "zp": 22.1,
            "exptime_sec": 30.0,
        },
    ]
    ctx: EntityContext = {"campaign_id": "c1", "frame_catalog": catalog}
    out = network_rollup.produce(ctx)
    assert "network.sky_coverage" in out
    assert all(k.startswith("network.") for k in out)
