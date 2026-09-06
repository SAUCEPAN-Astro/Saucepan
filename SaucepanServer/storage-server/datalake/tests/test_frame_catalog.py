"""Unit tests for storage/frame_catalog.py — the L1 sky/time catalog CRUD."""

from __future__ import annotations

from datetime import datetime, timedelta, timezone

import pytest


@pytest.fixture()
def catalog_db(tmp_path, monkeypatch):
    db_path = tmp_path / "catalog.db"
    monkeypatch.setenv("DATABASE_URL", f"sqlite:///{db_path}")
    import db as db_mod

    db_mod._engine = None
    db_mod._SessionLocal = None
    db_mod.init_db()
    yield
    db_mod._engine = None
    db_mod._SessionLocal = None


def test_upsert_requires_telescope_and_object_key(catalog_db):
    from storage.frame_catalog import upsert_frame_catalog

    with pytest.raises(ValueError):
        upsert_frame_catalog({"telescope_id": "node1"})
    with pytest.raises(ValueError):
        upsert_frame_catalog({"object_key": "c/frame.fits"})


def test_upsert_inserts_new_row(catalog_db):
    from storage.frame_catalog import upsert_frame_catalog

    row = upsert_frame_catalog(
        {
            "upload_id": "u1",
            "telescope_id": "node1",
            "object_key": "campaign/frame.fits",
            "ra_deg": 10.5,
            "dec_deg": -5.2,
            "campaign_id": "camp1",
        }
    )
    assert row["telescope_id"] == "node1"
    assert row["upload_id"] == "u1"
    assert row["ra_deg"] == 10.5
    assert row["created_at"] is not None


def test_upsert_updates_existing_row_by_upload_id(catalog_db):
    from storage.frame_catalog import upsert_frame_catalog

    first = upsert_frame_catalog(
        {
            "upload_id": "u1",
            "telescope_id": "node1",
            "object_key": "campaign/frame.fits",
            "ra_deg": 1.0,
        }
    )
    second = upsert_frame_catalog(
        {
            "upload_id": "u1",
            "telescope_id": "node1",
            "object_key": "campaign/frame.fits",
            "ra_deg": 2.0,
        }
    )
    assert first["id"] == second["id"]
    assert second["ra_deg"] == 2.0


def test_upsert_falls_back_to_catalog_id_when_no_upload_id(catalog_db):
    from storage.frame_catalog import upsert_frame_catalog

    first = upsert_frame_catalog(
        {
            "id": "fixed-id-1",
            "telescope_id": "node1",
            "object_key": "campaign/frame.fits",
        }
    )
    second = upsert_frame_catalog(
        {
            "id": "fixed-id-1",
            "telescope_id": "node1",
            "object_key": "campaign/frame2.fits",
        }
    )
    assert first["id"] == "fixed-id-1" == second["id"]
    assert second["object_key"] == "campaign/frame2.fits"


def test_list_for_campaign_filters_and_orders(catalog_db):
    from storage.frame_catalog import list_for_campaign, upsert_frame_catalog

    now = datetime.now(timezone.utc)
    upsert_frame_catalog(
        {
            "upload_id": "u1",
            "telescope_id": "node1",
            "object_key": "a.fits",
            "campaign_id": "camp1",
            "date_obs": now,
        }
    )
    upsert_frame_catalog(
        {
            "upload_id": "u2",
            "telescope_id": "node1",
            "object_key": "b.fits",
            "campaign_id": "camp1",
            "date_obs": now - timedelta(days=1),
        }
    )
    upsert_frame_catalog(
        {
            "upload_id": "u3",
            "telescope_id": "node1",
            "object_key": "c.fits",
            "campaign_id": "other-campaign",
        }
    )

    rows = list_for_campaign("camp1")
    assert len(rows) == 2
    # ascending date_obs, nulls last: earliest date first
    assert rows[0]["object_key"] == "b.fits"
    assert rows[1]["object_key"] == "a.fits"


def test_list_for_campaign_empty_for_unknown(catalog_db):
    from storage.frame_catalog import list_for_campaign

    assert list_for_campaign("nope") == []


def test_query_by_sky_time_ra_dec_box(catalog_db):
    from storage.frame_catalog import query_by_sky_time, upsert_frame_catalog

    upsert_frame_catalog(
        {
            "upload_id": "u1",
            "telescope_id": "node1",
            "object_key": "a.fits",
            "ra_deg": 10.0,
            "dec_deg": 20.0,
        }
    )
    upsert_frame_catalog(
        {
            "upload_id": "u2",
            "telescope_id": "node1",
            "object_key": "b.fits",
            "ra_deg": 100.0,
            "dec_deg": -50.0,
        }
    )

    rows = query_by_sky_time(ra_deg=10.0, dec_deg=20.0, radius_deg=1.0)
    assert len(rows) == 1
    assert rows[0]["object_key"] == "a.fits"


def test_query_by_sky_time_filter_name(catalog_db):
    from storage.frame_catalog import query_by_sky_time, upsert_frame_catalog

    upsert_frame_catalog(
        {
            "upload_id": "u1",
            "telescope_id": "node1",
            "object_key": "a.fits",
            "filter": "V",
        }
    )
    upsert_frame_catalog(
        {
            "upload_id": "u2",
            "telescope_id": "node1",
            "object_key": "b.fits",
            "filter": "R",
        }
    )
    rows = query_by_sky_time(filter_name="V")
    assert len(rows) == 1
    assert rows[0]["object_key"] == "a.fits"


def test_query_by_sky_time_date_range(catalog_db):
    from storage.frame_catalog import query_by_sky_time, upsert_frame_catalog

    now = datetime.now(timezone.utc)
    upsert_frame_catalog(
        {
            "upload_id": "u1",
            "telescope_id": "node1",
            "object_key": "a.fits",
            "date_obs": now,
        }
    )
    upsert_frame_catalog(
        {
            "upload_id": "u2",
            "telescope_id": "node1",
            "object_key": "old.fits",
            "date_obs": now - timedelta(days=30),
        }
    )
    rows = query_by_sky_time(date_from=now - timedelta(days=1))
    assert len(rows) == 1
    assert rows[0]["object_key"] == "a.fits"


def test_query_by_sky_time_no_filters_returns_all(catalog_db):
    from storage.frame_catalog import query_by_sky_time, upsert_frame_catalog

    upsert_frame_catalog({"upload_id": "u1", "telescope_id": "node1", "object_key": "a.fits"})
    upsert_frame_catalog({"upload_id": "u2", "telescope_id": "node1", "object_key": "b.fits"})
    rows = query_by_sky_time()
    assert len(rows) == 2
