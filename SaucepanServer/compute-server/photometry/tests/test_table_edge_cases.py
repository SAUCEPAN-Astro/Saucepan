"""Edge cases for photometry.table row-building helpers (#422)."""

from __future__ import annotations

from photometry import table


def test_build_table_row_extra_fields_merged():
    row = table.build_table_row(time="t", mag=1.0, extra={"custom_field": "value"})
    assert row["custom_field"] == "value"


def test_build_table_row_no_check_star_or_extra_omits_keys():
    row = table.build_table_row(time="t", mag=1.0)
    assert "check_star" not in row
    assert "check_minus_comp" not in row


def test_row_from_lp_airmass_invalid_value_falls_back_to_none():
    row = table.row_from_lp({"lp.inst_mag": 10.0}, hdr={"SP_AIRMASS": "not-a-number"})
    assert row["airmass"] is None


def test_row_from_lp_mag_prefers_std_mag_over_delta_over_inst():
    row = table.row_from_lp({"lp.std_mag": 1.0, "lp.delta_mag": 2.0, "lp.inst_mag": 3.0})
    assert row["mag"] == 1.0

    row2 = table.row_from_lp({"lp.delta_mag": 2.0, "lp.inst_mag": 3.0})
    assert row2["mag"] == 2.0

    row3 = table.row_from_lp({"lp.inst_mag": 3.0})
    assert row3["mag"] == 3.0


def test_row_from_lp_comp_stars_skip_non_dict_entries():
    ctx = {"campaign_comp_stars": ["not-a-dict", {"id": "c1", "role": "comp"}]}
    row = table.row_from_lp({}, ctx=ctx)
    assert len(row["comp_stars"]) == 1
    assert row["comp_stars"][0]["id"] == "c1"


def test_row_from_lp_comp_stars_excludes_check_role():
    ctx = {
        "campaign_comp_stars": [
            {"id": "c1", "role": "comp"},
            {"id": "k1", "role": "check"},
        ]
    }
    row = table.row_from_lp({}, ctx=ctx)
    ids = [c["id"] for c in row["comp_stars"]]
    assert "c1" in ids
    assert "k1" not in ids


def test_row_from_lp_no_ctx_comp_stars_falls_back_to_lp_comp_id():
    row = table.row_from_lp({"lp.comp_id": "comp-x", "lp.comp_ref_mag": 11.0}, ctx={})
    assert row["comp_stars"] == [{"id": "comp-x", "ref_mag": 11.0}]


def test_row_from_lp_time_falls_back_through_chain():
    row = table.row_from_lp({}, hdr={}, ctx={"time": "ctx-time"})
    assert row["time"] == "ctx-time"

    row2 = table.row_from_lp({}, hdr={}, ctx={"date_obs": "ctx-date"})
    assert row2["time"] == "ctx-date"

    row3 = table.row_from_lp({}, hdr={"DATE-OBS": "hdr-date"}, ctx={})
    assert row3["time"] == "hdr-date"


def test_row_from_lp_filter_fallback_chain():
    row = table.row_from_lp({}, hdr={"FILTER": "g"}, ctx={})
    assert row["filter"] == "g"

    row2 = table.row_from_lp({}, hdr={}, ctx={"filter": "ctx-filter"})
    assert row2["filter"] == "ctx-filter"


def test_row_from_lp_no_check_id_omits_check_star():
    row = table.row_from_lp({"lp.inst_mag": 1.0})
    assert "check_star" not in row


def test_validate_row_shape_reports_all_missing():
    missing = table.validate_row_shape({})
    assert set(missing) == set(table.REQUIRED_FIELDS)


def test_validate_row_shape_empty_when_all_present():
    row = {k: None for k in table.REQUIRED_FIELDS}
    assert table.validate_row_shape(row) == []
