"""Cross-telescope consistency harness — "one good telescope" metric (#418).

Given ≥2 telescopes observing the same target at the same epoch, compare
standard-system magnitudes (after ZP + colour-term transform) within stated
error bars and report residual scatter.

Pass/fail uses reduced chi-square against the inverse-variance-weighted mean,
not pairwise 1σ tests (those have ~32% false-fail per pair and explode with N).

Failure mode: systematic residual above the chi² gate = residual instrument
signature — do not claim instrument-agnostic / "one good telescope" output.

Pytest entry points live under ``pipeline/tests/`` and ``photometry/tests/``
so #408/#409/#410/#415 can treat this as their science acceptance gate.
"""

from __future__ import annotations

import math
import typing

from photometry import transform as phot_transform

Observation = dict[str, typing.Any]

# Default reduced-χ² ceiling. Under H0 (consistent mags, honest σ), E[χ²_red]≈1.
# 2.0 allows mild error underestimation / leftover systematics without the old
# pairwise-1σ false-fail rate (~98% for 5 telescopes). Callers may tighten via
# chi2_red_max (alias: sigma_threshold).
DEFAULT_CHI2_RED_MAX = 2.0


def evaluate_consistency(
    observations: list[Observation],
    *,
    require_transform: bool = True,
    chi2_red_max: float | None = None,
    sigma_threshold: float | None = None,
) -> dict[str, typing.Any]:
    """
    Compare multi-telescope standard magnitudes.

    Each observation needs::

        {
          "telescope_id": str,
          "std_mag": float,
          "std_mag_err": float,   # 1σ stated error bar
          "transform_applied": bool,  # optional; required True if require_transform
        }

    Gate: inverse-variance-weighted mean → reduced χ² = χ²/(N-1). Pass when
    ``chi2_red <= chi2_red_max`` (default ``DEFAULT_CHI2_RED_MAX``).
    ``sigma_threshold`` is an alias for ``chi2_red_max``.

    Pairwise deltas stay in the report as outlier diagnostics only — they do
    not drive ``pass``.
    """
    if len(observations) < 2:
        raise ValueError("consistency harness requires ≥2 telescope observations")

    if chi2_red_max is not None and sigma_threshold is not None:
        raise ValueError("pass only one of chi2_red_max or sigma_threshold")
    threshold = DEFAULT_CHI2_RED_MAX
    if chi2_red_max is not None:
        threshold = float(chi2_red_max)
    elif sigma_threshold is not None:
        threshold = float(sigma_threshold)
    if threshold <= 0:
        raise ValueError("chi2_red_max / sigma_threshold must be > 0")

    cleaned: list[Observation] = []
    for i, obs in enumerate(observations):
        if "std_mag" not in obs or "std_mag_err" not in obs:
            raise ValueError(f"observation[{i}] missing std_mag / std_mag_err")
        if "telescope_id" not in obs:
            raise ValueError(f"observation[{i}] missing telescope_id")
        if require_transform and not obs.get("transform_applied", False):
            raise ValueError(
                f"observation[{i}] ({obs['telescope_id']}) missing transform_applied; "
                "colour-term path (#419) must run before the #418 gate"
            )
        err = float(obs["std_mag_err"])
        if err <= 0 or not math.isfinite(err):
            raise ValueError(f"observation[{i}] ({obs['telescope_id']}) std_mag_err must be > 0")
        cleaned.append(obs)

    mags = [float(o["std_mag"]) for o in cleaned]
    errs = [float(o["std_mag_err"]) for o in cleaned]
    n = len(mags)

    inv_var = [1.0 / (e * e) for e in errs]
    w_sum = sum(inv_var)
    mean = sum(m * w for m, w in zip(mags, inv_var)) / w_sum

    # Sample RMS about the weighted mean (diagnostic; general formula covers n=2).
    residual_scatter = math.sqrt(sum((m - mean) ** 2 for m in mags) / (n - 1))

    chi2 = sum(((m - mean) / e) ** 2 for m, e in zip(mags, errs))
    chi2_red = chi2 / (n - 1)
    gate_pass = chi2_red <= threshold

    pairs: list[dict[str, typing.Any]] = []
    for i in range(n):
        for j in range(i + 1, n):
            delta = abs(mags[i] - mags[j])
            bar = math.hypot(errs[i], errs[j])
            pairs.append(
                {
                    "a": cleaned[i]["telescope_id"],
                    "b": cleaned[j]["telescope_id"],
                    "delta_mag": round(delta, 6),
                    "combined_err": round(bar, 6),
                    # Diagnostic only — not the pass/fail gate.
                    "within_1sigma": delta <= bar,
                }
            )

    report: dict[str, typing.Any] = {
        "n_telescopes": n,
        "telescope_ids": [o["telescope_id"] for o in cleaned],
        "mean_std_mag": round(mean, 6),
        "weighted_mean_std_mag": round(mean, 6),
        "residual_scatter": round(residual_scatter, 6),
        "chi2": round(chi2, 6),
        "chi2_red": round(chi2_red, 6),
        "chi2_red_max": threshold,
        "dof": n - 1,
        "pairs": pairs,
        "pass": gate_pass,
        "metric": "cross_telescope_reduced_chi2",
        "north_star": "one_good_telescope",
        "gates": ["#408", "#409", "#410", "#415"],
    }
    if not gate_pass:
        report["failure_mode"] = (
            "residual_instrument_signature: reduced chi-square "
            f"{chi2_red:.3f} exceeds gate {threshold:g} — systematic "
            "cross-telescope disagreement; block 'one good telescope' claims"
        )
        report["pass"] = False
    return report


def evaluate_from_instrumental(
    rows: list[dict[str, typing.Any]],
    *,
    mag_err: float = 0.02,
    chi2_red_max: float | None = None,
    sigma_threshold: float | None = None,
) -> dict[str, typing.Any]:
    """
    Synthetic / offline path: each row has instrumental mag + colour + profile.

    Row keys: ``telescope_id``, ``inst_mag``, ``color_index``, ``profile``
    (loaded profile dict), optional ``airmass``, ``mag_err``, ``zp_override``,
    ``color_term_err``, ``color_index_err``.
    """
    observations: list[Observation] = []
    for row in rows:
        profile = row["profile"]
        row_err = float(row.get("mag_err", mag_err))
        applied = phot_transform.apply_transform(
            float(row["inst_mag"]),
            color_index=float(row["color_index"]),
            profile=profile,
            airmass=row.get("airmass"),
            zp_override=row.get("zp_override"),
            mag_err=row_err,
            color_term_err=row.get("color_term_err"),
            color_index_err=row.get("color_index_err"),
        )
        std_err = float(applied.get("std_mag_err", row_err))
        observations.append(
            {
                "telescope_id": row.get("telescope_id") or profile["telescope_id"],
                "std_mag": applied["std_mag"],
                "std_mag_err": std_err,
                "transform_applied": True,
                "transform": applied,
            }
        )
    report = evaluate_consistency(
        observations,
        require_transform=True,
        chi2_red_max=chi2_red_max,
        sigma_threshold=sigma_threshold,
    )
    report["observations"] = observations
    return report
