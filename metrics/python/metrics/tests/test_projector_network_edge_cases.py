"""Edge-case tests for metrics.projectors.network.rollup_network.

Complements tests/test_network_rollup.py, which covers the happy path.
"""

from __future__ import annotations

from metrics.projectors.network import rollup_network


def test_rollup_network_none_frame_catalog_returns_empty():
    assert rollup_network("camp") == {}


def test_rollup_network_ignores_non_dict_rows():
    catalog = [
        "not-a-dict",
        {
            "telescope_id": "node_a",
            "ra_deg": 10.0,
            "dec_deg": 20.0,
            "date_obs": "2024-01-15T22:00:00Z",
        },
    ]
    out = rollup_network("camp", frame_catalog=catalog)
    assert out["_n_frames"] == 1


def test_rollup_network_all_non_dict_rows_returns_empty():
    assert rollup_network("camp", frame_catalog=["a", "b", 1, None]) == {}


def test_rollup_network_single_frame_cadence_none():
    catalog = [
        {
            "telescope_id": "node_a",
            "ra_deg": 10.0,
            "dec_deg": 20.0,
            "date_obs": "2024-01-15T22:00:00Z",
        }
    ]
    out = rollup_network("camp", frame_catalog=catalog)
    assert out["network.cadence_median"] is None
    assert out["network.cadence_std"] is None


def test_rollup_network_missing_ra_dec_excluded_from_sky_coverage():
    catalog = [
        {"telescope_id": "node_a", "date_obs": "2024-01-15T22:00:00Z"},  # no ra/dec
    ]
    out = rollup_network("camp", frame_catalog=catalog)
    assert out["network.sky_coverage"] == 0.0


def test_rollup_network_missing_date_obs_excluded_from_cadence():
    catalog = [
        {"telescope_id": "node_a", "ra_deg": 1.0, "dec_deg": 1.0},
        {"telescope_id": "node_a", "ra_deg": 1.0, "dec_deg": 1.0},
    ]
    out = rollup_network("camp", frame_catalog=catalog)
    assert out["network.cadence_median"] is None


def test_rollup_network_malformed_date_obs_is_skipped():
    catalog = [
        {"telescope_id": "node_a", "ra_deg": 1.0, "dec_deg": 1.0, "date_obs": "not-a-date"},
        {
            "telescope_id": "node_a",
            "ra_deg": 1.0,
            "dec_deg": 1.0,
            "date_obs": "2024-01-15T22:00:00Z",
        },
    ]
    out = rollup_network("camp", frame_catalog=catalog)
    # Only one valid timestamp parsed -> no gap to compute cadence from.
    assert out["network.cadence_median"] is None


def test_rollup_network_no_zp_values_no_offsets():
    catalog = [
        {
            "telescope_id": "node_a",
            "ra_deg": 1.0,
            "dec_deg": 1.0,
            "date_obs": "2024-01-15T22:00:00Z",
        },
    ]
    out = rollup_network("camp", frame_catalog=catalog)
    assert out["network.zp_offset_per_tele"] is None
    assert out["network.cross_site_lc_residual"] is None
    assert out["network.sigma_sys_floor"] is None


def test_rollup_network_outlier_node_flagged_when_offset_large():
    catalog = [
        {
            "telescope_id": "node_a",
            "ra_deg": 1.0,
            "dec_deg": 1.0,
            "date_obs": "2024-01-15T22:00:00Z",
            "zp": 22.0,
        },
        {
            "telescope_id": "node_b",
            "ra_deg": 1.0,
            "dec_deg": 1.0,
            "date_obs": "2024-01-15T22:01:00Z",
            "zp": 22.5,
        },
    ]
    out = rollup_network("camp", frame_catalog=catalog)
    assert out["network.outlier_node_flag"]  # non-empty: offset > 0.05 mag


def test_rollup_network_no_outliers_flag_is_none():
    catalog = [
        {
            "telescope_id": "node_a",
            "ra_deg": 1.0,
            "dec_deg": 1.0,
            "date_obs": "2024-01-15T22:00:00Z",
            "zp": 22.0,
        },
        {
            "telescope_id": "node_b",
            "ra_deg": 1.0,
            "dec_deg": 1.0,
            "date_obs": "2024-01-15T22:01:00Z",
            "zp": 22.01,
        },
    ]
    out = rollup_network("camp", frame_catalog=catalog)
    assert out["network.outlier_node_flag"] is None


def test_rollup_network_stack_eligible_completeness_zero_when_none_eligible():
    catalog = [
        {
            "telescope_id": "node_a",
            "ra_deg": 1.0,
            "dec_deg": 1.0,
            "date_obs": "2024-01-15T22:00:00Z",
            "stack_eligible": False,
        },
    ]
    out = rollup_network("camp", frame_catalog=catalog)
    assert out["network.completeness"] == 0.0
    assert out["network.purity"] == 0.0


def test_rollup_network_storage_per_obs_average_of_size_bytes():
    catalog = [
        {
            "telescope_id": "node_a",
            "ra_deg": 1.0,
            "dec_deg": 1.0,
            "date_obs": "2024-01-15T22:00:00Z",
            "size_bytes": 100,
        },
        {
            "telescope_id": "node_a",
            "ra_deg": 1.0,
            "dec_deg": 1.0,
            "date_obs": "2024-01-15T22:01:00Z",
            "size_bytes": 200,
        },
    ]
    out = rollup_network("camp", frame_catalog=catalog)
    assert out["network.storage_per_obs"] == 150.0


def test_rollup_network_storage_per_obs_none_when_absent():
    catalog = [
        {
            "telescope_id": "node_a",
            "ra_deg": 1.0,
            "dec_deg": 1.0,
            "date_obs": "2024-01-15T22:00:00Z",
        },
    ]
    out = rollup_network("camp", frame_catalog=catalog)
    assert out["network.storage_per_obs"] is None


def test_rollup_network_target_id_used_directly_when_present():
    catalog = [
        {"telescope_id": "node_a", "target_id": "M31", "date_obs": "2024-01-15T22:00:00Z"},
        {"telescope_id": "node_b", "target_id": "M31", "date_obs": "2024-01-15T22:01:00Z"},
    ]
    out = rollup_network("camp", frame_catalog=catalog)
    assert out["network.stereo_confirm_count"] == 1.0


def test_rollup_network_expected_nodes_override_changes_redundancy_flags():
    catalog = [
        {
            "telescope_id": "node_a",
            "ra_deg": 1.0,
            "dec_deg": 1.0,
            "date_obs": "2024-01-15T22:00:00Z",
        },
        {
            "telescope_id": "node_b",
            "ra_deg": 1.0,
            "dec_deg": 1.0,
            "date_obs": "2024-01-15T22:01:00Z",
        },
    ]
    out = rollup_network("camp", frame_catalog=catalog, expected_nodes=5)
    # With expected=5 and only 2 nodes observed, redundancy is under-observed.
    assert out["network.field_under_observed"] > 0.0


def test_rollup_network_template_cadence_ratio_computed():
    catalog = [
        {
            "telescope_id": "node_a",
            "ra_deg": 1.0,
            "dec_deg": 1.0,
            "date_obs": "2024-01-15T22:00:00Z",
        },
        {
            "telescope_id": "node_a",
            "ra_deg": 1.0,
            "dec_deg": 1.0,
            "date_obs": "2024-01-15T22:10:00Z",
        },
    ]
    out = rollup_network("camp", frame_catalog=catalog, template_cadence_sec=300.0)
    assert out["network.cadence_vs_template"] == 2.0


def test_rollup_network_campaign_id_echoed_in_internal_field():
    catalog = [
        {
            "telescope_id": "node_a",
            "ra_deg": 1.0,
            "dec_deg": 1.0,
            "date_obs": "2024-01-15T22:00:00Z",
        },
    ]
    out = rollup_network("my-campaign", frame_catalog=catalog)
    assert out["_campaign_id"] == "my-campaign"
    assert out["_n_telescopes"] == 1
    assert out["_n_nights"] == 1
