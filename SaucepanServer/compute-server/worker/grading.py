"""
Thin Lambda wrapper around shared ``grading`` package (Track F).

FITS I/O + quality.assess_fits live here; scoring math is in ``grading/``.
"""

from __future__ import annotations

import argparse
import json
import logging
import typing

from grading import constants
from grading.emulator_policy import classify_frame
from grading.fits_reader import read_sp_headers
from grading.orchestrate import build_grade_payload

from .context import extract_task_context, resolve_staged_path

try:
    from saucepan_pipeline import quality
except ImportError:
    from . import quality  # editable install not yet on PYTHONPATH

logger = logging.getLogger(__name__)

GRADER_VERSION = f"{constants.GRADER_VERSION}-lambda"


def grade_frame(
    fits_path: str,
    task_context: dict[str, typing.Any],
    *,
    update_fits: bool = False,
    measure_fwhm_if_missing: bool = False,
) -> dict[str, typing.Any]:
    """
    Grade a single frame. Returns subscores + headline 0–100.

    Args:
        fits_path: Local path to FITS file.
        task_context: Task/upload metadata (from SQS message or datalake).
        update_fits: Write SP_SNR etc. via quality.assess_fits.
        measure_fwhm_if_missing: Measure PSF FWHM in-process when SP_FWHM missing.
    """
    headers = read_sp_headers(fits_path)
    # Always assess from pixels; when FWHM is missing and requested, force
    # in-process measurement into the FITS (#409) before scoring.
    need_fwhm = measure_fwhm_if_missing and headers.get("sp_fwhm") is None
    quality_metrics = quality.assess_fits(
        fits_path,
        update_fits=update_fits or need_fwhm,
    )
    if need_fwhm:
        measured = quality_metrics.get("fwhm_arcsec")
        if measured is not None and float(measured) > 0:
            headers = read_sp_headers(fits_path)
            logger.info("Measured FWHM in-process: %.3f arcsec", float(measured))
        else:
            logger.info(
                "measure_fwhm_if_missing requested but FWHM fit returned no value; "
                "using neutral score"
            )

    classification = classify_frame(headers, task_context)

    return build_grade_payload(
        task_context,
        quality_metrics=quality_metrics,
        headers=headers,
        grader_version=GRADER_VERSION,
        classification=classification,
    )


def handler(event: dict, context=None) -> dict:
    """
    Lambda entry for grade_frame action (direct invoke or after SQS unwrap).

    Event fields: path or s3_key (local path until s3_io exists), plus task context.
    """
    try:
        raw_path = event.get("path")
        if not raw_path and event.get("s3_key"):
            raw_path = event["s3_key"]
        if not raw_path:
            return {"statusCode": 400, "body": {"error": "Need 'path' or 's3_key'"}}
        try:
            path = str(resolve_staged_path(raw_path))
        except (ValueError, FileNotFoundError) as exc:
            return {"statusCode": 400, "body": {"error": str(exc)}}

        task_context = extract_task_context(event)

        result = grade_frame(
            path,
            task_context,
            update_fits=event.get("update_fits", False),
            measure_fwhm_if_missing=event.get("measure_fwhm_if_missing", False),
        )
        return {"statusCode": 200, "body": result}
    except Exception as exc:
        logger.exception("grade_frame failed")
        return {"statusCode": 500, "body": {"error": str(exc)}}


def main() -> None:
    parser = argparse.ArgumentParser(description="Grade a FITS frame (Track F)")
    parser.add_argument("--input", "-i", required=True, help="Input FITS file")
    parser.add_argument("--context", "-c", help="JSON file with task context")
    parser.add_argument("--output", "-o", help="Output JSON path (default: stdout)")
    args = parser.parse_args()

    ctx: dict[str, typing.Any] = {}
    if args.context:
        with open(args.context, encoding="utf-8") as fh:
            ctx = json.load(fh)

    result = grade_frame(args.input, ctx)
    output = json.dumps(result, indent=2)
    if args.output:
        with open(args.output, "w", encoding="utf-8") as fh:
            fh.write(output)
        print(f"Written {args.output}")
    else:
        print(output)


if __name__ == "__main__":
    logging.basicConfig(level=logging.INFO, format="%(levelname)s: %(message)s")
    main()
