"""Saucepan science pipeline — imaging stages SSOT."""

from saucepan_pipeline.background import get_background_rms, subtract_background
from saucepan_pipeline.calibration import calibrate_image
from saucepan_pipeline.driver import STAGE_ORDER, prepare_frames_for_stack, run_stack_pipeline
from saucepan_pipeline.header_reader import (
    OAHeaders,
    SPHeaders,
    read_oa_headers,
    read_sp_headers,
    validate_headers,
)
from saucepan_pipeline.psf_match import match_psf, select_target_psf
from saucepan_pipeline.quality import (
    assess_fits,
    assess_quality,
    estimate_fwhm,
    write_quality_headers,
)
from saucepan_pipeline.reproject_frame import reproject_frame
from saucepan_pipeline.stacking import stack_fits_files, stack_frames

__all__ = [
    "STAGE_ORDER",
    "calibrate_image",
    "stack_frames",
    "stack_fits_files",
    "run_stack_pipeline",
    "prepare_frames_for_stack",
    "assess_quality",
    "assess_fits",
    "estimate_fwhm",
    "write_quality_headers",
    "subtract_background",
    "get_background_rms",
    "match_psf",
    "select_target_psf",
    "reproject_frame",
    "SPHeaders",
    "read_sp_headers",
    "OAHeaders",
    "read_oa_headers",
    "validate_headers",
]
