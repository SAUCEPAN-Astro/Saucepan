"""Imaging pipeline driver — SSOT stage order for stacking (#408).

Order: calibration → background → quality → PSF match → reproject → stack.
Photometric scaling (#410) runs after reproject, before the weighted mean.
"""

from __future__ import annotations

import logging

import numpy as np

from saucepan_pipeline.background import subtract_background
from saucepan_pipeline.calibration import (
    apply_calibration_steps,
    apply_gain_conversion,
    remove_cosmic_rays,
    update_calstat,
    update_header_with_calibration,
)
from saucepan_pipeline.psf_match import match_psf, select_target_psf, write_psf_headers
from saucepan_pipeline.quality import assess_quality
from saucepan_pipeline.stacking.combine import stack_frames
from saucepan_pipeline.stacking.frames import load_frame
from saucepan_pipeline.stacking.metrics import get_pixel_scale_from_header
from saucepan_pipeline.stacking.models import FrameInfo, StackResult
from saucepan_pipeline.stacking.output import save_stacked_fits
from saucepan_pipeline.target_photometry import measure_target_flux

logger = logging.getLogger(__name__)

STAGE_ORDER = (
    "calibration",
    "background",
    "quality",
    "psf_match",
    "reproject",
    "stack",
)


def _calibrate_in_memory(
    frame: FrameInfo,
    *,
    remove_crs: bool = False,
    config: dict | None = None,
) -> FrameInfo:
    """Apply bias/dark/flat (if present on disk paths) + gain conversion."""
    config = config or {}
    calstat = str(frame.header.get("SP_CALSTAT", "NONE") or "NONE")
    filter_name = str(frame.header.get("SP_FILTER") or frame.header.get("FILTER") or "UNKNOWN")

    data, applied, apply_dark, apply_flat, apply_bias = apply_calibration_steps(
        frame.data,
        calstat,
        config.get("bias_path"),
        config.get("dark_path"),
        config.get("flat_path"),
    )
    data, header = apply_gain_conversion(data, frame.header, frame.path, config)
    # remove_cosmic_rays defaults OFF (opt-in) — same default as
    # calibration.calibrate_image. It is now L.A.Cosmic (astroscrappy), which
    # spares PSF-shaped sources, but stays opt-in for parity with the stack
    # path. Runs only on explicit opt-in (remove_crs=True or the config flag).
    #
    # CR hybrid policy (#413): the *filled* array flows on to background /
    # quality / PSF-match so those stages see no CR spikes, but the detection
    # mask is OR-ed into frame.mask so the stack drops those pixels to weight
    # 0 rather than trusting the global-median fill value.
    if remove_crs or config.get("remove_cosmic_rays", False):
        data, cr_mask = remove_cosmic_rays(data, header=header)
        if cr_mask is not None and cr_mask.any():
            frame.mask = cr_mask if frame.mask is None else (frame.mask | cr_mask)
            if "cosmic_ray" not in frame.mask_sources:
                frame.mask_sources = [*frame.mask_sources, "cosmic_ray"]

    new_calstat = update_calstat(calstat, applied)
    update_header_with_calibration(
        header, filter_name, apply_dark, apply_flat, apply_bias, new_calstat
    )
    frame.data = data.astype(np.float32)
    frame.header = header
    return frame


def _apply_background(frame: FrameInfo, *, box_size: int = 50) -> FrameInfo:
    bg_sub, _bg_map = subtract_background(frame.data, box_size=box_size)
    frame.data = bg_sub
    # background / noise re-measured in _apply_quality from the subtracted pixels
    return frame


def _apply_quality(frame: FrameInfo) -> FrameInfo:
    """Re-measure SNR/noise/FWHM from pixels after cal+background (#292/#409)."""
    ps = frame.pixel_scale_arcsec or get_pixel_scale_from_header(frame.header) or 1.0
    frame.pixel_scale_arcsec = float(ps)
    q = assess_quality(frame.data, pixel_scale_arcsec=frame.pixel_scale_arcsec)
    frame.noise_adu = float(q["noise_adu"])
    frame.background = float(q["background"])
    frame.snr = float(q["snr"])
    frame.saturated_pixels = int(q["saturated_pixels"])
    frame.fwhm_arcsec = float(q.get("fwhm_arcsec") or 0.0)
    # Pipeline-owned write — never trust inbound SP_FWHM.
    frame.header["SP_FWHM"] = (frame.fwhm_arcsec, "PSF FWHM arcsec (measured in-process)")
    frame.header["SP_SNR"] = (frame.snr, "Signal-to-noise ratio")
    frame.header["SP_BGNOI"] = (frame.noise_adu, "Background noise (ADU)")

    # Position-anchored target flux/SNR (#471) - additive alongside the
    # whole-image snr/signal_adu above. Frames without a resolvable sky
    # target (e.g. bias/dark/flat calibration frames) just keep the
    # dataclass defaults (None) - that's the expected path, not an error.
    ra = frame.header.get("SP_RA")
    dec = frame.header.get("SP_DEC")
    if ra is not None and dec is not None:
        try:
            ra_f, dec_f = float(ra), float(dec)
        except (TypeError, ValueError):
            ra_f = dec_f = None
        if ra_f is not None and dec_f is not None:
            tgt = measure_target_flux(frame.data, frame.wcs, ra_f, dec_f)
            if tgt.get("ok"):
                frame.target_flux = float(tgt["flux"])
                if frame.noise_adu > 0:
                    frame.target_snr = frame.target_flux / frame.noise_adu
    return frame


def _apply_psf_match(frames: list[FrameInfo]) -> float:
    """Convolve sharper frames up to max accepted FWHM. Returns target FWHM."""
    fwhm_list = [f.fwhm_arcsec for f in frames if f.fwhm_arcsec > 0]
    if not fwhm_list:
        return 0.0
    target = float(select_target_psf(fwhm_list))
    for frame in frames:
        ps = frame.pixel_scale_arcsec
        src = frame.fwhm_arcsec
        if src <= 0 or ps <= 0 or src >= target:
            continue
        matched = match_psf(frame.data, src, target, ps)
        frame.data = matched
        kernel_sigma = float(np.sqrt((target / ps / 2.3548) ** 2 - (src / ps / 2.3548) ** 2))
        write_psf_headers(frame.header, src, target, kernel_sigma)
        # After matching, effective FWHM is the target.
        frame.fwhm_arcsec = target
        frame.header["SP_FWHM"] = (target, "PSF FWHM arcsec after PSF match")
    return target


def prepare_frames_for_stack(
    paths: list[str],
    telescope_ids: list[str] | None = None,
    *,
    max_psf_fwhm: float | None = None,
    box_size: int = 50,
    calibration_config: dict | None = None,
) -> tuple[list[FrameInfo], list[dict], dict]:
    """
    Run calibration → background → quality → PSF match on each input.

    Returns:
        (accepted_frames, quality_rejects, stage_meta)
    """
    if telescope_ids and len(telescope_ids) != len(paths):
        raise ValueError("telescope_ids length must match paths")

    accepted: list[FrameInfo] = []
    rejects: list[dict] = []
    config = calibration_config or {}

    for i, path in enumerate(paths):
        tid = telescope_ids[i] if telescope_ids else None
        frame = load_frame(path, tid)

        # 1. Calibration
        frame = _calibrate_in_memory(frame, config=config)

        # 2. Background
        frame = _apply_background(frame, box_size=box_size)

        # 3. Quality (in-process FWHM)
        frame = _apply_quality(frame)
        logger.info(
            'Prepared %s: tele=%s noise=%.3f SNR=%.1f FWHM=%.3f"',
            path,
            frame.telescope_id,
            frame.noise_adu,
            frame.snr,
            frame.fwhm_arcsec,
        )

        if max_psf_fwhm is not None and frame.fwhm_arcsec > float(max_psf_fwhm):
            rejects.append(
                {
                    "telescope_id": frame.telescope_id,
                    "path": path,
                    "fwhm_arcsec": frame.fwhm_arcsec,
                    "rejected": True,
                    "reject_reason": (
                        f'FWHM {frame.fwhm_arcsec:.3f}" > max_psf_fwhm {max_psf_fwhm}'
                    ),
                    "weight": 0.0,
                    "weight_pct": 0.0,
                    "noise_adu": frame.noise_adu,
                    "snr": frame.snr,
                    "exptime": frame.exptime,
                    "pixel_scale": frame.pixel_scale_arcsec,
                    "fwhm_weight_factor": 0.0,
                    "n_rejected_pixels": 0,
                }
            )
            logger.info(
                'Rejected %s: FWHM %.3f" > max_psf_fwhm %s',
                path,
                frame.fwhm_arcsec,
                max_psf_fwhm,
            )
            continue

        accepted.append(frame)

    target_psf = 0.0
    if accepted:
        # 4. PSF match (native pixel space, before reproject)
        target_psf = _apply_psf_match(accepted)

    meta = {
        "stages": list(STAGE_ORDER),
        "target_psf_fwhm_arcsec": target_psf,
        "n_quality_rejected": len(rejects),
    }
    return accepted, rejects, meta


def run_stack_pipeline(
    paths: list[str],
    output_path: str,
    telescope_ids: list[str] | None = None,
    *,
    use_highest_resolution_grid: bool = True,
    sigma_clip: float = 3.0,
    auto_crop: bool = True,
    weight_by_fwhm: bool = True,
    max_psf_fwhm: float | None = None,
    photometric_scale: bool = True,
    box_size: int = 50,
    calibration_config: dict | None = None,
) -> dict:
    """
    Full imaging stack driver used by POST /v1/stack and the demo.

    Stages: calibration → background → quality → PSF match → reproject → stack
    (with photometric scaling before the weighted mean when enabled).
    """
    accepted, quality_rejects, meta = prepare_frames_for_stack(
        paths,
        telescope_ids,
        max_psf_fwhm=max_psf_fwhm,
        box_size=box_size,
        calibration_config=calibration_config,
    )
    if len(accepted) < 1:
        raise ValueError(
            f"No frames survived quality gate "
            f"(rejected={len(quality_rejects)}, max_psf_fwhm={max_psf_fwhm})"
        )
    if len(accepted) == 1 and len(paths) > 1:
        logger.warning("Only one frame survived quality gate; stacking single frame")

    # 5–6. Reproject + photometric scale + weighted stack
    result: StackResult = stack_frames(
        accepted,
        use_highest_resolution_grid=use_highest_resolution_grid,
        sigma_clip=sigma_clip,
        auto_crop=auto_crop,
        weight_by_fwhm=weight_by_fwhm,
        photometric_scale=photometric_scale,
    )

    # Fold quality rejects into provenance / counts.
    result.provenance = list(result.provenance) + quality_rejects
    result.n_rejected = result.n_rejected + len(quality_rejects)

    save_stacked_fits(result, accepted, output_path)

    from saucepan_pipeline.stacking.metrics import summarize_stack_quality

    m = summarize_stack_quality(result, accepted)

    return {
        "output": output_path,
        "shape": list(result.science.shape),
        "n_frames_used": result.n_frames,
        "n_frames_rejected": result.n_rejected,
        "stack_noise_adu": m["stack_noise_adu"],
        "stack_snr": m["stack_snr"],
        "best_single_snr": m["best_single_snr"],
        "snr_gain": m["snr_gain"],
        "theoretical_max": m["theoretical_max"],
        "efficiency": m["efficiency"],
        "stack_snr_target": m["stack_snr_target"],
        "best_single_snr_target": m["best_single_snr_target"],
        "snr_gain_target": m["snr_gain_target"],
        "efficiency_target": m["efficiency_target"],
        "provenance": result.provenance,
        "stages": meta["stages"],
        "target_psf_fwhm_arcsec": meta["target_psf_fwhm_arcsec"],
    }


__all__ = [
    "STAGE_ORDER",
    "prepare_frames_for_stack",
    "run_stack_pipeline",
]
