"""Tests for upload_context, upload_catalog, task_snapshot, lp_products, governance producers."""

from __future__ import annotations

import metrics.producers.governance as governance
import metrics.producers.lp_products as lp_products
import metrics.producers.task_snapshot as task_snapshot
import metrics.producers.upload_catalog as upload_catalog
import metrics.producers.upload_context as upload_context
from metrics.observation import EntityContext

# ---------------------------------------------------------------------------
# upload_context
# ---------------------------------------------------------------------------


def test_upload_context_empty_ctx_returns_empty():
    assert upload_context.produce({}) == {}


def test_upload_context_clock_source_normalizes_case_and_whitespace():
    out = upload_context.produce({"clock_source": " ntp "})
    assert out["frame.timesys"] == "NTP"


def test_upload_context_clock_source_unknown_value_maps_to_unknown():
    out = upload_context.produce({"clock_source": "quartz"})
    assert out["frame.timesys"] == "UNKNOWN"


def test_upload_context_no_clock_source_key_omits_timesys():
    out = upload_context.produce({"upload_id": "u1"})
    assert "frame.timesys" not in out


def test_upload_context_omits_observer_identity():
    out = upload_context.produce({"observer_display_name": "Alice", "user_id": "usr-1"})
    assert "frame.observer" not in out


def test_upload_context_detector_temp_zero_is_included():
    out = upload_context.produce({"detector_temp_c": 0.0})
    assert out["frame.detector_temp"] == 0.0


# ---------------------------------------------------------------------------
# upload_catalog
# ---------------------------------------------------------------------------


def test_upload_catalog_uses_catalog_dict_when_present():
    ctx: EntityContext = {
        "_catalog": {
            "upload_id": "u1",
            "upload_status": "complete",
            "size_bytes": 1024,
            "staged_path": "/tmp/staged.fits",
        },
        # Top-level ctx values must be ignored once _catalog is present.
        "upload_id": "ignored",
    }
    out = upload_catalog.produce(ctx)
    assert out["ops.upload_id"] == "u1"
    assert out["ops.upload_status"] == "complete"
    assert out["ops.upload_size_bytes"] == 1024
    assert "ops.staging_path" not in out


def test_upload_catalog_falls_back_to_ctx_when_no_catalog():
    ctx: EntityContext = {"upload_id": "u2", "staged_path": "/tmp/f.fits"}
    out = upload_catalog.produce(ctx)
    assert out == {"ops.upload_id": "u2"}


def test_upload_catalog_empty_ctx_returns_empty():
    assert upload_catalog.produce({}) == {}


def test_upload_catalog_non_dict_catalog_falls_back_to_ctx():
    ctx: EntityContext = {"_catalog": "not-a-dict", "upload_id": "u3"}
    out = upload_catalog.produce(ctx)
    assert out == {"ops.upload_id": "u3"}


# ---------------------------------------------------------------------------
# task_snapshot
# ---------------------------------------------------------------------------


def test_task_snapshot_no_snapshot_returns_empty():
    assert task_snapshot.produce({}) == {}


def test_task_snapshot_empty_dict_snapshot_returns_empty():
    assert task_snapshot.produce({"task_snapshot": {}}) == {}


def test_task_snapshot_direct_field_mapping():
    ctx: EntityContext = {"task_snapshot": {"name": "M31 survey", "priority": 5}}
    out = task_snapshot.produce(ctx)
    assert out["task.name"] == "M31 survey"
    assert out["task.priority"] == 5


def test_task_snapshot_arcsec_suffix_fallback():
    # max_psf_fwhm_arcsec missing; falls back to bare "max_psf_fwhm".
    ctx: EntityContext = {"task_snapshot": {"max_psf_fwhm": 2.5}}
    out = task_snapshot.produce(ctx)
    assert out["task.max_psf_fwhm"] == 2.5


def test_task_snapshot_min_altitude_fallback():
    ctx: EntityContext = {"task_snapshot": {"min_altitude": 30.0}}
    out = task_snapshot.produce(ctx)
    assert out["task.min_altitude"] == 30.0


def test_task_snapshot_assigned_tele_id_fallback():
    ctx: EntityContext = {"task_snapshot": {"assigned_tele_id": "T9"}}
    out = task_snapshot.produce(ctx)
    assert out["task.assigned_tele_id"] == "T9"


def test_task_snapshot_prefers_primary_key_over_fallback():
    ctx: EntityContext = {"task_snapshot": {"max_psf_fwhm_arcsec": 1.1, "max_psf_fwhm": 9.9}}
    out = task_snapshot.produce(ctx)
    assert out["task.max_psf_fwhm"] == 1.1


# ---------------------------------------------------------------------------
# lp_products
# ---------------------------------------------------------------------------


def test_lp_products_no_result_returns_empty():
    assert lp_products.produce({}) == {}


def test_lp_products_non_dict_result_returns_empty():
    assert lp_products.produce({"_lp_result": "nope"}) == {}


def test_lp_products_filters_to_known_keys_only():
    ctx: EntityContext = {
        "_lp_result": {
            "lp.source_snr": 10.0,
            "lp.delta_mag": -0.02,
            "not.a.real.key": 999,
        }
    }
    out = lp_products.produce(ctx)
    assert out == {"lp.source_snr": 10.0, "lp.delta_mag": -0.02}


def test_lp_products_empty_result_dict_returns_empty():
    assert lp_products.produce({"_lp_result": {}}) == {}


# ---------------------------------------------------------------------------
# governance
# ---------------------------------------------------------------------------


def test_governance_ignores_ctx_argument():
    out1 = governance.produce({"upload_id": "u1"})
    out2 = governance.produce({})
    assert out1 == out2


def test_governance_deploy_env_defaults_dev(monkeypatch):
    monkeypatch.delenv("GOV_DEPLOY_ENV", raising=False)
    monkeypatch.delenv("DEPLOY_ENV", raising=False)
    out = governance.produce({})
    assert out["gov.deploy_env"] == "dev"


def test_governance_deploy_env_prefers_gov_specific_var(monkeypatch):
    monkeypatch.setenv("DEPLOY_ENV", "staging")
    monkeypatch.setenv("GOV_DEPLOY_ENV", "prod")
    out = governance.produce({})
    assert out["gov.deploy_env"] == "prod"


def test_governance_falls_back_to_generic_deploy_env(monkeypatch):
    monkeypatch.delenv("GOV_DEPLOY_ENV", raising=False)
    monkeypatch.setenv("DEPLOY_ENV", "staging")
    out = governance.produce({})
    assert out["gov.deploy_env"] == "staging"


def test_governance_pipeline_norm_ver_from_env(monkeypatch):
    monkeypatch.setenv("GOV_PIPELINE_NORM_VER", "9.9.9")
    out = governance.produce({})
    assert out["gov.pipeline_norm_ver"] == "9.9.9"


def test_governance_blank_env_values_are_dropped(monkeypatch):
    monkeypatch.setenv("GOV_CLIENT_VER", "   ")
    out = governance.produce({})
    assert "gov.client_ver" not in out


def test_governance_pipeline_module_ver_has_static_default(monkeypatch):
    monkeypatch.delenv("GOV_PIPELINE_MODULE_VER", raising=False)
    out = governance.produce({})
    assert out["gov.pipeline_module_ver"] == "stacking 1.0.0"
