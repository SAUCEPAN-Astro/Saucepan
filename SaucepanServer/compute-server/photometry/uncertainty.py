"""Per-measurement magnitude uncertainty (photon + sky + read + aperture).

One home for the CCD-equation error budget used by both the frame ZP fit and
the LP differential-photometry product. Kept deliberately small: a single
closed-form function plus a couple of thin wrappers.
"""

from __future__ import annotations

import math
import typing

# 2.5 / ln(10) — converts a fractional flux error to a magnitude error.
POGSON = 1.0857362047581294


def mag_err_from_flux(
    flux: float,
    *,
    npix: float,
    sky: float = 0.0,
    gain: float = 1.0,
    rdnoise: float = 0.0,
    sky_err: float = 0.0,
) -> float | None:
    """Magnitude uncertainty for an aperture sum.

    ``sigma_mag = 1.0857 * sqrt(flux/gain + npix*(sky/gain + rdnoise^2)
                               + (npix*sky_err)^2) / flux``

    Terms, in order: source photon noise, per-pixel sky photon + read noise
    over the aperture, and the aperture's shared sky-estimate uncertainty.
    ``flux`` and ``sky`` are in ADU, ``gain`` in e-/ADU, ``rdnoise`` /
    ``sky_err`` in e- and ADU respectively.

    Returns ``None`` when the inputs cannot yield a finite, positive error
    (non-positive flux, non-finite arguments) — callers must fail closed
    rather than invent a number.
    """
    for val in (flux, npix, sky, gain, rdnoise, sky_err):
        if val is None or not math.isfinite(float(val)):
            return None
    if flux <= 0.0 or npix <= 0.0 or gain <= 0.0:
        return None

    source_var = flux / gain
    sky_read_var = npix * (max(sky, 0.0) / gain + rdnoise**2)
    sky_est_var = (npix * max(sky_err, 0.0)) ** 2
    total = source_var + sky_read_var + sky_est_var
    if total <= 0.0 or not math.isfinite(total):
        return None

    return POGSON * math.sqrt(total) / flux


def ccd_error_terms(
    flux: float,
    *,
    npix: float,
    sky: float = 0.0,
    gain: float = 1.0,
    rdnoise: float = 0.0,
    sky_err: float = 0.0,
) -> dict[str, float] | None:
    """The CCD-equation error split into its named magnitude terms.

    Same inputs and algebra as :func:`mag_err_from_flux`, but returns the
    individual contributions so the full budget (#206) can list each one::

        {"photon": σ_photon, "sky": σ_sky, "read": σ_read}

    ``sky`` here folds the per-pixel sky *photon* noise and the aperture's
    shared sky-estimate uncertainty (both scale with the sky background);
    ``read`` is the detector read-noise term. Quadrature-summing the three
    reproduces :func:`mag_err_from_flux` exactly. Returns ``None`` on the same
    fail-closed conditions.
    """
    for val in (flux, npix, sky, gain, rdnoise, sky_err):
        if val is None or not math.isfinite(float(val)):
            return None
    if flux <= 0.0 or npix <= 0.0 or gain <= 0.0:
        return None

    scale = POGSON / flux
    photon = scale * math.sqrt(flux / gain)
    sky_var = npix * (max(sky, 0.0) / gain) + (npix * max(sky_err, 0.0)) ** 2
    sky_term = scale * math.sqrt(sky_var)
    read_term = scale * math.sqrt(npix * rdnoise**2)
    if not all(math.isfinite(v) for v in (photon, sky_term, read_term)):
        return None
    return {"photon": photon, "sky": sky_term, "read": read_term}


# Ordered term names for the full per-measurement error budget (#206):
#   σ²_total = σ²_photon + σ²_sky + σ²_read + σ²_scint
#            + σ²_transform + σ²_red + σ²_pier
BUDGET_TERMS = ("photon", "sky", "read", "scint", "transform", "red", "pier")


def uncertainty_budget(
    *,
    photon: float | None = None,
    sky: float | None = None,
    read: float | None = None,
    scint: float | None = None,
    transform: float | None = None,
    red: float | None = None,
    pier: float | None = None,
    sigma_sys: float | None = None,
    domain_cell: str | None = None,
) -> dict[str, typing.Any]:
    """Assemble the full per-measurement magnitude error budget (#206).

    Each of the seven terms is a 1σ magnitude value **or** ``None`` when it was
    not measured (photon/sky/read from :func:`ccd_error_terms`; ``scint`` from
    ``photometry.scintillation.sigma_scint_mag``; ``transform`` from
    ``photometry.transform.apply_transform_coeffs``; ``red`` from
    ``photometry.rednoise``; ``pier`` = this site's ΔZP scatter from the #205
    coverage map). A ``None`` term is reported, not silently treated as zero.

    Returns::

        {
          "terms": {name: {"sigma": v|None, "var": v²|None}},
          "sigma_total": √Σ var  (over measured terms only),
          "sigma_total_var": Σ var,
          "measured": [names...], "missing": [names...],
          "sigma_sys": sigma_sys, "domain_cell": domain_cell,
        }

    ``sigma_sys`` (per pier / domain cell) is passed through for the product
    metadata; when given it is *also* folded into ``sigma_total`` as an eighth
    term so a release carries an honest floor.
    """
    supplied = {
        "photon": photon,
        "sky": sky,
        "read": read,
        "scint": scint,
        "transform": transform,
        "red": red,
        "pier": pier,
    }
    terms: dict[str, dict[str, float | None]] = {}
    total_var = 0.0
    measured: list[str] = []
    missing: list[str] = []
    for name in BUDGET_TERMS:
        v = supplied[name]
        if v is None or not math.isfinite(float(v)) or float(v) < 0.0:
            terms[name] = {"sigma": None, "var": None}
            missing.append(name)
            continue
        v = float(v)
        terms[name] = {"sigma": v, "var": v * v}
        total_var += v * v
        measured.append(name)

    if sigma_sys is not None and math.isfinite(float(sigma_sys)) and float(sigma_sys) >= 0.0:
        total_var += float(sigma_sys) ** 2

    return {
        "terms": terms,
        "sigma_total": math.sqrt(total_var) if total_var > 0.0 else None,
        "sigma_total_var": total_var if total_var > 0.0 else None,
        "measured": measured,
        "missing": missing,
        "sigma_sys": float(sigma_sys) if sigma_sys is not None else None,
        "domain_cell": domain_cell,
    }


def combine_in_quadrature(*errs: float | None) -> float | None:
    """Root-sum-square of independent error terms; ``None`` if none are usable."""
    usable = [float(e) for e in errs if e is not None and math.isfinite(float(e)) and e >= 0.0]
    if not usable:
        return None
    return math.sqrt(sum(e * e for e in usable))


def differential_mag_err(
    target_err: float | None,
    comp_errs: typing.Iterable[float | None],
) -> float | None:
    """Error on ``target - ensemble(comps)``.

    The comparison ensemble is treated as an inverse-variance-weighted mean, so
    its contribution is ``1 / sqrt(sum(1/e_i^2))``. Combined in quadrature with
    the target's own error.
    """
    inv_var = 0.0
    for e in comp_errs:
        if e is None:
            continue
        e = float(e)
        if math.isfinite(e) and e > 0.0:
            inv_var += 1.0 / (e * e)
    ensemble_err = math.sqrt(1.0 / inv_var) if inv_var > 0.0 else None
    return combine_in_quadrature(target_err, ensemble_err)
