"""Build FITS headers and write multi-extension stacked output."""

import logging

import numpy as np
from astropy.io import fits

from saucepan_pipeline.stacking.metrics import summarize_stack_quality
from saucepan_pipeline.stacking.models import FrameInfo, StackResult

logger = logging.getLogger(__name__)


def build_output_header(result: StackResult, frames: list[FrameInfo]) -> fits.Header:
    """Build FITS header with provenance and quality metadata (SP_ prefix)."""
    header = fits.Header()

    header.update(result.ref_wcs.to_header())

    header["SP_NSTACK"] = (result.n_frames, "Frames used in stack")
    header["SP_NREJECT"] = (result.n_rejected, "Frames rejected")
    units = {
        str(frame.header.get("SP_BUNIT")).strip().lower()
        for frame in frames
        if frame.header.get("SP_BUNIT")
    }
    if len(units) > 1:
        raise ValueError(f"cannot stack frames with mixed physical units: {sorted(units)}")
    # Direct FrameInfo callers historically omit SP_BUNIT and are treated as
    # electron-valued.  Calibrated frames carry the authoritative unit; an
    # ADU-only calibration therefore remains ADU instead of being mislabeled.
    output_unit = next(iter(units), "electron")
    header["SP_BUNIT"] = (output_unit, "Physical units")
    header["SP_PIPE"] = ("stacking 1.0.0", "Pipeline module")
    first_frame = frames[0].path if frames else "unknown"
    header["SP_REFF"] = (first_frame, "Reference frame for reprojection")

    for i, p in enumerate(result.provenance):
        prefix = f"FRM{i + 1:03d}"
        header[f"{prefix}_ID"] = (p["telescope_id"], "Telescope ID")
        header[f"{prefix}_WGT"] = (p["weight_pct"], "Weight contribution %")
        header[f"{prefix}_SNR"] = (p["snr"], "Frame SNR")
        header[f"{prefix}_NOISE"] = (p["noise_adu"], "Frame noise (ADU)")
        header[f"{prefix}_FWHM"] = (p["fwhm_arcsec"], "Frame FWHM (arcsec)")
        header[f"{prefix}_REJ"] = (p["rejected"], "Frame rejected")
        header[f"{prefix}_PIXSC"] = (p["pixel_scale"], "Frame pixel scale (arcsec/px)")

    flat = result.science[~np.isnan(result.science)]
    if len(flat) > 0:
        m = summarize_stack_quality(result, frames)
        header["SP_SNR"] = (m["stack_snr"], "Stack signal-to-noise ratio")
        header["SP_BGMD"] = (m["background"], "Stack background median (ADU)")
        header["SP_BGNOI"] = (m["stack_noise_adu"], "Stack background noise (ADU, from noise_map)")
        header["SP_SNRGN"] = (m["snr_gain"], "SNR improvement vs best single frame")
        header["SP_THMAX"] = (m["theoretical_max"], "Theoretical max sqrt(Nframes)")
        header["SP_EFF"] = (m["efficiency"], "Stacking efficiency (gain / theoretical_max)")
        header["SP_STARS"] = (m["star_pixels"], "Star pixel count in stack")
        header["SP_AREA"] = (int((~np.isnan(result.science)).sum()), "Valid pixels in stack")
        header["SP_CROP"] = (str(result.crop_slice), "Crop slice (y1,y2,x1,x2)")
        header["SP_COV"] = (int(result.coverage_map.max()), "Max frames per pixel")

        # Position-anchored equivalents (#471) - additive only, never
        # overwrite SP_SNR/SP_EFF/SP_SNRGN/etc above. 'N/A' (FITS has no
        # native optional-float type) whenever no frame carried a
        # resolvable SP_RA/SP_DEC, so a missing measurement can't be
        # mistaken for a real zero.
        header["SP_STFLX"] = (
            m["stack_target_flux"] if m["stack_target_flux"] is not None else "N/A",
            "Stack flux at target sky position (aperture)",
        )
        header["SP_SNRT"] = (
            m["stack_snr_target"] if m["stack_snr_target"] is not None else "N/A",
            "Stack SNR at target sky position (aperture)",
        )
        header["SP_SNGT"] = (
            m["best_single_snr_target"] if m["best_single_snr_target"] is not None else "N/A",
            "Best single-frame target-anchored SNR",
        )
        header["SP_SNRGT"] = (
            m["snr_gain_target"] if m["snr_gain_target"] is not None else "N/A",
            "Target-anchored SNR gain vs best single frame",
        )
        header["SP_EFFT"] = (
            m["efficiency_target"] if m["efficiency_target"] is not None else "N/A",
            "Target-anchored stacking efficiency",
        )

    if frames and all(f.sp_emulator for f in frames):
        header["SP_EMULATOR"] = (1, "Synthetic stack (all inputs emulator)")
        header["SP_TIER"] = ("emulator", "Data tier (not production science)")

    return header


def save_stacked_fits(result: StackResult, frames: list[FrameInfo], output_path: str):
    """Write stacked result as multi-extension FITS."""
    header = build_output_header(result, frames)

    science_hdu = fits.PrimaryHDU(result.science, header=header)
    weight_hdu = fits.ImageHDU(result.weight_map, name="WEIGHT")
    noise_hdu = fits.ImageHDU(result.noise_map, name="NOISE")
    coverage_hdu = fits.ImageHDU(result.coverage_map.astype(np.int32), name="COVERAGE")

    if result.provenance:
        col1 = fits.Column(
            name="telescope_id", format="20A", array=[p["telescope_id"] for p in result.provenance]
        )
        col2 = fits.Column(
            name="weight_pct", format="E", array=[p["weight_pct"] for p in result.provenance]
        )
        col3 = fits.Column(name="snr", format="E", array=[p["snr"] for p in result.provenance])
        col4 = fits.Column(
            name="noise_adu", format="E", array=[p["noise_adu"] for p in result.provenance]
        )
        col5 = fits.Column(
            name="rejected", format="L", array=[p["rejected"] for p in result.provenance]
        )
        col6 = fits.Column(
            name="fwhm_arcsec", format="E", array=[p["fwhm_arcsec"] for p in result.provenance]
        )
        prov_hdu = fits.BinTableHDU.from_columns(
            [col1, col2, col3, col4, col5, col6], name="PROVENANCE"
        )
    else:
        prov_hdu = fits.BinTableHDU.from_columns([], name="PROVENANCE")

    hdul = fits.HDUList([science_hdu, weight_hdu, noise_hdu, coverage_hdu, prov_hdu])
    hdul.writeto(output_path, overwrite=True)
    logger.info("Stacked FITS written: %s", output_path)
