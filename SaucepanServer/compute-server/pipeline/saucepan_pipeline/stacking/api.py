"""High-level stacking API, Lambda handler, and CLI entry point.

``stack_fits_files`` delegates to the imaging pipeline driver (#408):
calibration → background → quality → PSF match → reproject → stack.
"""

import argparse
import json
import logging

logger = logging.getLogger(__name__)


def stack_fits_files(
    paths: list[str],
    output_path: str,
    telescope_ids: list[str] | None = None,
    use_highest_resolution_grid: bool = True,
    sigma_clip: float = 3.0,
    auto_crop: bool = True,
    weight_by_fwhm: bool = True,
    max_psf_fwhm: float | None = None,
    photometric_scale: bool = True,
    calibration_config: dict | None = None,
) -> dict:
    """
    Stack multiple FITS files into a single output.

    Runs the full imaging pipeline driver (not load+combine only).
    High-level API for CLI, Lambda, and POST /v1/stack.
    """
    # Lazy import avoids circular import: driver → stacking.combine → api.
    from saucepan_pipeline.driver import run_stack_pipeline

    return run_stack_pipeline(
        paths,
        output_path,
        telescope_ids=telescope_ids,
        use_highest_resolution_grid=use_highest_resolution_grid,
        sigma_clip=sigma_clip,
        auto_crop=auto_crop,
        weight_by_fwhm=weight_by_fwhm,
        max_psf_fwhm=max_psf_fwhm,
        photometric_scale=photometric_scale,
        calibration_config=calibration_config,
    )


def handler(event: dict, context=None) -> dict:
    """
    Lambda handler for stacking.

    Event format:
    {
        "action": "stack",
        "frames": [{"path": "...", "telescope_id": "..."}],
        "output_path": "/tmp/stacked.fits",
        "config": {"use_highest_resolution_grid": true, "max_psf_fwhm": 5.0, ...}
    }
    """
    try:
        action = event.get("action", "stack")
        if action != "stack":
            return {"statusCode": 400, "body": {"error": f"Unknown action: {action}"}}

        frame_configs = event.get("frames", [])
        output_path = event.get("output_path", "/tmp/stacked.fits")
        config = event.get("config", {})

        paths = [f["path"] for f in frame_configs]
        tids = [f.get("telescope_id") for f in frame_configs]

        summary = stack_fits_files(
            paths,
            output_path,
            telescope_ids=tids,
            use_highest_resolution_grid=config.get("use_highest_resolution_grid", True),
            sigma_clip=config.get("sigma_clip", 3.0),
            auto_crop=config.get("auto_crop", True),
            weight_by_fwhm=config.get("weight_by_fwhm", True),
            max_psf_fwhm=config.get("max_psf_fwhm"),
            photometric_scale=config.get("photometric_scale", True),
        )

        return {"statusCode": 200, "body": summary}

    except Exception as e:
        logger.exception("Stacking failed")
        return {"statusCode": 500, "body": {"error": str(e)}}


def main():
    parser = argparse.ArgumentParser(description="Stack FITS files")
    parser.add_argument("--frames", "-f", nargs="+", required=True, help="Input FITS files")
    parser.add_argument("--output", "-o", required=True, help="Output FITS path")
    parser.add_argument("--telescope-ids", nargs="*", help="Telescope IDs (1:1 with frames)")
    parser.add_argument(
        "--use-cdk-grid",
        action="store_true",
        default=True,
        help="Use highest-resolution telescope as reference",
    )
    parser.add_argument("--sigma-clip", type=float, default=3.0, help="Sigma clip threshold")
    parser.add_argument("--no-crop", action="store_true", help="Disable auto-crop")
    parser.add_argument(
        "--max-psf-fwhm",
        type=float,
        default=None,
        help="Reject frames with measured FWHM above this (arcsec)",
    )
    parser.add_argument(
        "--no-photometric-scale",
        action="store_true",
        help="Disable per-frame photometric scaling before combine",
    )
    args = parser.parse_args()

    summary = stack_fits_files(
        args.frames,
        args.output,
        telescope_ids=args.telescope_ids,
        use_highest_resolution_grid=args.use_cdk_grid,
        sigma_clip=args.sigma_clip,
        auto_crop=not args.no_crop,
        max_psf_fwhm=args.max_psf_fwhm,
        photometric_scale=not args.no_photometric_scale,
    )

    print(json.dumps(summary, indent=2))


if __name__ == "__main__":
    logging.basicConfig(level=logging.INFO, format="%(levelname)s: %(message)s")
    main()
