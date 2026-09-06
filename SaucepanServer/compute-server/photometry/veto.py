"""Veto ledger for heterogeneous ensemble photometry (#206 / #113).

The compute-server export gate calls :func:`evaluate_veto` on every candidate
measurement with the campaign's ``veto_policy`` (see
``SaucepanServer/contracts/photometry/veto_policy.schema.json``). A *hard*
condition drops the measurement from every merged product; a *soft* condition
keeps it but excludes it from ensemble combination.

One rule is not policy-configurable: **a blind coadd of frames on different
transform paths is always refused** (:func:`assert_same_transform_path`) — mix
``Clear->V`` and native ``V`` into one "mag" and the systematic colour term
leaks straight into the light curve. Frames that cannot share a transform path
are emitted as separate native channels.

Kept deliberately small: pure functions over plain dicts, stdlib only.
"""

from __future__ import annotations

import math
import typing

Measurement = dict[str, typing.Any]

# Declarable conditions (mirrors the contract schema). Flags carry no value;
# ``*_gt`` conditions compare a measurement field against a threshold.
_FLAG_CONDITIONS = ("filter_mismatch", "bad_night")
_THRESHOLD_CONDITIONS = {
    "transform_residual_gt": "transform_residual",
    "zp_rms_gt": "zp_rms",
    "airmass_gt": "airmass",
    "pier_delta_z_gt": "pier_delta_z",
}
KNOWN_CONDITIONS = set(_FLAG_CONDITIONS) | set(_THRESHOLD_CONDITIONS)

_BAD_NIGHT_FLAGS = {"nonphot", "non-photometric", "bad", "cloudy", "cloud"}


class CrossPathCoaddError(ValueError):
    """Raised when measurements on incompatible transform paths would be coadded."""


def _norm_condition(cond: typing.Any) -> tuple[str, float | None]:
    """Normalise a schema condition to ``(name, threshold_or_None)``."""
    if isinstance(cond, str):
        if cond not in _FLAG_CONDITIONS:
            raise ValueError(f"unknown flag veto condition {cond!r}")
        return cond, None
    if isinstance(cond, typing.Mapping) and len(cond) == 1:
        (name, value), = cond.items()
        if name in _FLAG_CONDITIONS:
            return name, None
        if name in _THRESHOLD_CONDITIONS:
            return name, float(value)
        raise ValueError(f"unknown veto condition {name!r}")
    raise ValueError(f"malformed veto condition: {cond!r}")


def _flag_triggered(name: str, m: Measurement) -> tuple[bool, typing.Any]:
    if name == "filter_mismatch":
        requested = m.get("requested_filter") or m.get("requested_filter_family")
        got = m.get("filter_family") or m.get("filter")
        if requested is None or got is None:
            return False, None
        mismatch = str(requested) != str(got)
        # a registered transform path rescues an otherwise-mismatched filter
        if mismatch and m.get("transform_path"):
            return False, got
        return mismatch, got
    if name == "bad_night":
        flag = str(m.get("night_flag") or m.get("photometric_flag") or "").strip().lower()
        return flag in _BAD_NIGHT_FLAGS, flag or None
    return False, None


def _threshold_triggered(
    name: str, threshold: float, m: Measurement
) -> tuple[bool, typing.Any]:
    field = _THRESHOLD_CONDITIONS[name]
    raw = m.get(field)
    if raw is None or not math.isfinite(float(raw)):
        return False, None
    value = abs(float(raw)) if name == "pier_delta_z_gt" else float(raw)
    return value > threshold, value


def _eval_conditions(
    conditions: typing.Iterable[typing.Any], level: str, m: Measurement
) -> list[dict[str, typing.Any]]:
    hits: list[dict[str, typing.Any]] = []
    for cond in conditions or []:
        name, threshold = _norm_condition(cond)
        if threshold is None:
            fired, value = _flag_triggered(name, m)
        else:
            fired, value = _threshold_triggered(name, threshold, m)
        if fired:
            hits.append(
                {"rule": name, "level": level, "value": value, "threshold": threshold}
            )
    return hits


def evaluate_veto(
    measurement: Measurement,
    veto_policy: typing.Mapping[str, typing.Any] | None,
    transform_stats: typing.Mapping[str, typing.Any] | None = None,
) -> dict[str, typing.Any]:
    """Apply a campaign ``veto_policy`` to one measurement.

    Args:
        measurement: fields the conditions read — ``filter_family`` /
            ``requested_filter``, ``transform_path`` (registry id or ``None``),
            ``transform_residual``, ``zp_rms``, ``airmass``, ``pier_delta_z``,
            ``night_flag``.
        veto_policy: ``{"hard": [...], "soft": [...]}`` (schema
            ``veto_policy.schema.json``). ``None`` / ``{}`` => nothing vetoed.
        transform_stats: optional ``{path_id: {"residual_rms": ...}}``; when a
            measurement omits ``transform_residual`` it is filled from here.

    Returns:
        ``{"allow": bool, "vetoes": [{rule, level, value, threshold}],
           "channel": "ensemble" | "native_only" | "dropped"}``.
        ``allow`` is false iff a hard condition fired. A soft hit leaves
        ``allow`` true but sets ``channel="native_only"`` (keep as a
        single-pier note, out of the ensemble combination).
    """
    m = dict(measurement)
    if m.get("transform_residual") is None and transform_stats:
        stats = transform_stats.get(m.get("transform_path"))
        if isinstance(stats, typing.Mapping):
            m["transform_residual"] = stats.get("residual_rms") or stats.get(
                "transform_residual"
            )

    policy = veto_policy or {}
    hard_hits = _eval_conditions(policy.get("hard"), "hard", m)
    soft_hits = _eval_conditions(policy.get("soft"), "soft", m)
    vetoes = hard_hits + soft_hits

    allow = not hard_hits
    if not allow:
        channel = "dropped"
    elif soft_hits:
        channel = "native_only"
    else:
        channel = "ensemble"
    return {"allow": allow, "vetoes": vetoes, "channel": channel}


def transform_path_key(measurement: Measurement) -> typing.Any:
    """The identity a coadd must share: the transform path, or the native band.

    ``None`` transform path + a filter family is its own key ("native V"); two
    such frames on the same native band may be combined. A transformed frame
    keys on its transform id, so ``Clear->V`` never shares a bucket with
    native ``V``.
    """
    path = measurement.get("transform_path") or measurement.get("transform_id")
    if path:
        return ("transformed", path)
    band = measurement.get("filter_family") or measurement.get("filter")
    return ("native", band)


def assert_same_transform_path(measurements: typing.Sequence[Measurement]) -> typing.Any:
    """Refuse a blind cross-path coadd.

    Raises :class:`CrossPathCoaddError` when ``measurements`` span more than one
    :func:`transform_path_key` (e.g. transformed + untransformed, or two
    different transform ids). Returns the single shared key when they agree.
    """
    keys = {transform_path_key(m) for m in measurements}
    if len(keys) > 1:
        raise CrossPathCoaddError(
            "blind coadd across incompatible transform paths refused: "
            f"{sorted(map(str, keys))} — emit native channels separately"
        )
    return next(iter(keys)) if keys else None


def partition_channels(
    measurements: typing.Sequence[Measurement],
) -> dict[typing.Any, list[Measurement]]:
    """Group measurements into coadd-safe buckets by :func:`transform_path_key`."""
    out: dict[typing.Any, list[Measurement]] = {}
    for m in measurements:
        out.setdefault(transform_path_key(m), []).append(m)
    return out
