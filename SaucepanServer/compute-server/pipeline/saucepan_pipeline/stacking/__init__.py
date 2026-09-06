"""
Production-quality heterogeneous image stacking.

Public API: FrameInfo, StackResult, stack_frames, stack_fits_files, handler, main.
"""

from saucepan_pipeline.stacking.api import handler, main, stack_fits_files
from saucepan_pipeline.stacking.combine import estimate_photometric_scales, stack_frames
from saucepan_pipeline.stacking.models import FrameInfo, StackResult

__all__ = [
    "FrameInfo",
    "StackResult",
    "stack_frames",
    "stack_fits_files",
    "estimate_photometric_scales",
    "handler",
    "main",
]
