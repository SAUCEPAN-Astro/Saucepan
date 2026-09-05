"""Unit tests for coverage_metrics."""

from __future__ import annotations

from datetime import datetime, timedelta, timezone

from saucepan.campaigns import CampaignPack
from saucepan.coverage_metrics import (
    circular_lon_span_deg,
    compute_coverage_metrics,
    metrics_from_pack,
)


def _ts(minutes: float) -> str:
    base = datetime(2026, 1, 1, tzinfo=timezone.utc)
    return (base + timedelta(minutes=minutes)).isoformat().replace("+00:00", "Z")


def test_empty_deliveries():
    m = compute_coverage_metrics([])
    assert m.status == "insufficient_data"
    assert m.realized_max_gap_min is None
    assert m.sample_count == 0


def test_single_point():
    m = compute_coverage_metrics(
        [{"created_at": _ts(0), "telescope_id": "a", "status": "completed"}]
    )
    assert m.status == "insufficient_data"
    assert m.duty_cycle == 1.0
    assert m.realized_max_gap_min is None


def test_regular_cadence_gap():
    deliveries = [
        {"created_at": _ts(0), "telescope_id": "a", "status": "completed"},
        {"created_at": _ts(60), "telescope_id": "b", "status": "completed"},
        {"created_at": _ts(120), "telescope_id": "a", "status": "completed"},
    ]
    m = compute_coverage_metrics(deliveries, max_gap_intent_min=90, bin_minutes=30)
    assert m.realized_max_gap_min == 60
    assert m.status == "ok"
    assert m.contributing_telescopes["a"] == 2


def test_gap_exceeds_intent_soft_vs_hard():
    deliveries = [
        {"created_at": _ts(0), "telescope_id": "a", "status": "completed"},
        {"created_at": _ts(200), "telescope_id": "b", "status": "completed"},
    ]
    soft = compute_coverage_metrics(deliveries, max_gap_intent_min=60, mode="soft")
    hard = compute_coverage_metrics(deliveries, max_gap_intent_min=60, mode="hard")
    assert soft.status == "degraded"
    assert hard.status == "failed"


def test_dateline_longitude_span():
    span = circular_lon_span_deg([170.0, -170.0, 175.0])
    assert span < 30  # clustered near dateline


def test_missing_lon_span_none():
    m = compute_coverage_metrics(
        [
            {"created_at": _ts(0), "telescope_id": "a", "status": "completed"},
            {"created_at": _ts(10), "telescope_id": "b", "status": "completed"},
        ]
    )
    assert m.longitude_span_deg is None


def test_known_lons():
    m = compute_coverage_metrics(
        [
            {"created_at": _ts(0), "telescope_id": "west", "status": "completed"},
            {"created_at": _ts(10), "telescope_id": "east", "status": "completed"},
        ],
        telescope_longitudes={"west": -120.0, "east": 0.0},
        min_longitude_span_deg=90,
    )
    assert m.longitude_span_deg == 120.0
    assert m.status == "ok"


def test_stack_eligible_filter():
    deliveries = [
        {
            "created_at": _ts(0),
            "telescope_id": "a",
            "status": "completed",
            "stack_eligible": False,
        },
        {
            "created_at": _ts(10),
            "telescope_id": "b",
            "status": "completed",
            "stack_eligible": True,
        },
    ]
    m = compute_coverage_metrics(deliveries, stack_eligible_only=True)
    assert m.sample_count == 1


def test_metrics_from_pack():
    pack = CampaignPack.from_dict(
        {
            "name": "x",
            "coverage": {
                "enabled": True,
                "max_gap_min": 90,
                "mode": "soft",
                "min_sites": 2,
            },
            "season": {"kind": "continuous", "target_duty_cycle": 0.5},
            "targets": [{"ra": 1, "dec": 2}],
        }
    )
    m = metrics_from_pack(
        [
            {"created_at": _ts(0), "telescope_id": "a", "status": "completed"},
            {"created_at": _ts(30), "telescope_id": "b", "status": "completed"},
        ],
        pack,
    )
    assert m.vs_intent["max_gap_intent_min"] == 90
    assert m.vs_intent["target_duty_cycle"] == 0.5
    assert m.status == "ok"


def test_campaign_pack_season_roundtrip():
    pack = CampaignPack.from_dict(
        {
            "name": "s",
            "season": {"kind": "too", "urgency": "critical"},
            "coverage": {"enabled": True, "mode": "hard", "n_main": 2},
            "targets": [
                {
                    "ra": 1,
                    "dec": 2,
                    "filters": ["V"],
                    "exposure_sec": 1,
                    "frame_count": 10,
                    "saturation": {"strategy": "short", "max_exposure_sec": 2},
                }
            ],
        }
    )
    d = pack.to_dict()
    assert d["season"]["kind"] == "too"
    assert d["coverage"]["mode"] == "hard"
    pack.validate(for_publish=True)
