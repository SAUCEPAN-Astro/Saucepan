"""Edge cases for photometry.product_route (#422 campaign product routing)."""

from __future__ import annotations

from photometry import product_route


def test_product_from_ctx_none_ctx():
    assert product_route.product_from_ctx(None) == {}


def test_product_from_ctx_empty_ctx():
    assert product_route.product_from_ctx({}) == {}


def test_product_from_ctx_direct_product_dict():
    ctx = {"product": {"mode": "stack"}}
    assert product_route.product_from_ctx(ctx) == {"mode": "stack"}


def test_product_from_ctx_nested_in_task_snapshot():
    ctx = {"task_snapshot": {"product": {"mode": "time_bin"}}}
    assert product_route.product_from_ctx(ctx) == {"mode": "time_bin"}


def test_product_from_ctx_nested_in_pack():
    ctx = {"pack": {"product": {"mode": "stack"}}}
    assert product_route.product_from_ctx(ctx) == {"mode": "stack"}


def test_product_from_ctx_flat_product_mode_key():
    ctx = {"product_mode": "time_bin", "time_bin_frames": 5}
    result = product_route.product_from_ctx(ctx)
    assert result == {"mode": "time_bin", "time_bin_frames": 5}


def test_product_from_ctx_flat_mode_key_without_frames():
    ctx = {"mode": "per_frame"}
    assert product_route.product_from_ctx(ctx) == {"mode": "per_frame"}


def test_product_from_ctx_no_recognizable_keys():
    assert product_route.product_from_ctx({"unrelated": "value"}) == {}


def test_normalize_mode_invalid_mode_defaults_to_per_frame():
    assert product_route.normalize_mode({"mode": "not-a-real-mode"}) == "per_frame"


def test_normalize_mode_case_insensitive():
    assert product_route.normalize_mode({"mode": "STACK"}) == "stack"


def test_wants_stack_uses_ctx_when_product_not_given():
    ctx = {"product": {"mode": "stack"}}
    assert product_route.wants_stack(ctx=ctx) is True


def test_route_for_product_uses_ctx():
    ctx = {"product_mode": "stack"}
    assert product_route.route_for_product(ctx=ctx) == "stack"


def test_validate_product_none_and_empty_ok():
    assert product_route.validate_product(None) is None
    assert product_route.validate_product({}) is None


def test_validate_product_invalid_mode():
    err = product_route.validate_product({"mode": "not-a-mode"})
    assert err is not None
    assert "product.mode must be one of" in err


def test_validate_product_time_bin_frames_non_integer():
    err = product_route.validate_product({"mode": "time_bin", "time_bin_frames": "not-a-number"})
    assert err is not None
    assert "integer" in err


def test_validate_product_time_bin_frames_negative():
    err = product_route.validate_product({"mode": "time_bin", "time_bin_frames": -1})
    assert err is not None


def test_validate_product_time_bin_frames_exactly_two_ok():
    assert product_route.validate_product({"mode": "time_bin", "time_bin_frames": 2}) is None


def test_validate_product_bins_set_but_mode_not_time_bin_rejected():
    err = product_route.validate_product({"mode": "per_frame", "time_bin_frames": 5})
    assert err is not None
    assert "only valid when mode=time_bin" in err


def test_validate_product_bins_zero_or_empty_allowed_for_non_time_bin_mode():
    assert product_route.validate_product({"mode": "stack", "time_bin_frames": 0}) is None
    assert product_route.validate_product({"mode": "stack", "time_bin_frames": ""}) is None
    assert product_route.validate_product({"mode": "stack", "time_bin_frames": None}) is None
