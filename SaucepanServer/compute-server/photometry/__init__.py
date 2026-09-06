"""Async photometry pipeline — plate solve cache, ZP, LP stubs, JC transform."""

from photometry.consistency import evaluate_consistency, evaluate_from_instrumental
from photometry.lp import run_lp
from photometry.pipeline import run_photometry
from photometry.product_route import normalize_mode, route_for_product, wants_stack
from photometry.rednoise import binned_rms, pont_red_noise
from photometry.scintillation import sigma_scint_mag, sigma_scintillation
from photometry.table import TABLE_FIELDS, build_table_row, row_from_lp
from photometry.transform import (
    apply_transform,
    apply_transform_coeffs,
    find_transform,
    load_profile,
    load_registry,
    load_transform_by_hash,
)
from photometry.uncertainty import (
    BUDGET_TERMS,
    ccd_error_terms,
    differential_mag_err,
    mag_err_from_flux,
    uncertainty_budget,
)
from photometry.veto import (
    CrossPathCoaddError,
    assert_same_transform_path,
    evaluate_veto,
    partition_channels,
)
from photometry.wcs_stars import project_comp_stars, wcs_from_header
from photometry.zeropoint import fit_zeropoint, zeropoint_for_frame

__all__ = [
    "run_photometry",
    "run_lp",
    "apply_transform",
    "load_profile",
    "evaluate_consistency",
    "evaluate_from_instrumental",
    "route_for_product",
    "wants_stack",
    "normalize_mode",
    "build_table_row",
    "row_from_lp",
    "TABLE_FIELDS",
    "fit_zeropoint",
    "zeropoint_for_frame",
    "project_comp_stars",
    "wcs_from_header",
    "mag_err_from_flux",
    "differential_mag_err",
    "ccd_error_terms",
    "uncertainty_budget",
    "BUDGET_TERMS",
    "sigma_scintillation",
    "sigma_scint_mag",
    "apply_transform_coeffs",
    "find_transform",
    "load_registry",
    "load_transform_by_hash",
    "binned_rms",
    "pont_red_noise",
    "evaluate_veto",
    "assert_same_transform_path",
    "partition_channels",
    "CrossPathCoaddError",
]
