"""Tests for #422 product routing and photometry table shape."""

from __future__ import annotations

from photometry import product_route, table


def test_default_mode_is_per_frame_not_stack():
    assert product_route.normalize_mode(None) == "per_frame"
    assert product_route.normalize_mode({}) == "per_frame"
    assert product_route.wants_stack(None) is False
    assert product_route.route_for_product(None) == "photometry"


def test_stack_mode_routes_to_stack():
    prod = {"mode": "stack"}
    assert product_route.wants_stack(prod) is True
    assert product_route.route_for_product(prod) == "stack"


def test_time_bin_stays_on_photometry_route():
    prod = {"mode": "time_bin", "time_bin_frames": 10}
    assert product_route.validate_product(prod) is None
    assert product_route.wants_stack(prod) is False
    assert product_route.route_for_product(prod) == "photometry"


def test_time_bin_requires_frames():
    assert product_route.validate_product({"mode": "time_bin"}) is not None
    assert product_route.validate_product({"mode": "time_bin", "time_bin_frames": 1}) is not None


def test_time_domain_pack_fixture_does_not_force_stack():
    pack = {
        "product": {"mode": "per_frame"},
        "targets": [{"filters": ["V"], "exposure_sec": 30}],
    }
    assert "product" in pack
    assert pack["product"]["mode"] == "per_frame"
    assert product_route.wants_stack(pack["product"]) is False
    assert product_route.route_for_product(pack["product"]) == "photometry"


def test_photometry_table_row_shape():
    row = table.build_table_row(
        time="2026-07-29T00:00:00Z",
        mag=12.34,
        mag_err=0.02,
        comp_stars=[{"id": "c1", "ref_mag": 11.0}],
        airmass=1.2,
        filter_name="V",
        check_star={"id": "k1", "check_minus_comp": 0.01},
        check_minus_comp=0.01,
    )
    assert table.validate_row_shape(row) == []
    for key in table.REQUIRED_FIELDS:
        assert key in row
    assert "check_star" in row
    assert set(table.TABLE_FIELDS) >= set(table.REQUIRED_FIELDS)


def test_row_from_lp_maps_fields():
    lp = {
        "lp.delta_mag": 0.15,
        "lp.mag_err": 0.03,
        "lp.comp_id": "comp-a",
        "lp.comp_ref_mag": 11.2,
        "lp.check_id": "check-b",
        "lp.check_minus_comp": -0.01,
    }
    row = table.row_from_lp(
        lp,
        hdr={"SP_DATEOBS": "2026-01-01T12:00:00", "SP_FILTER": "R", "AIRMASS": 1.4},
        ctx={"campaign_comp_stars": [{"id": "comp-a", "ra": 1.0, "dec": 2.0, "mag": 11.2}]},
    )
    assert table.validate_row_shape(row) == []
    assert row["filter"] == "R"
    assert row["airmass"] == 1.4
    assert row["mag"] == 0.15
    assert row["check_star"]["id"] == "check-b"
