"""
normalize.py — Program 1: FITS metadata standardization.

Reads any FITS file, writes SP_-prefixed canonical headers to HDU[0],
preserves original headers verbatim in HDU[1] ORIGHDRS extension.

This program ONLY standardizes metadata (headers). It does not touch pixel
data for science purposes. Science processing (calibration, stacking) is
handled separately by compute-server ``saucepan_pipeline`` (or researcher SDK).

Interface:
    Library:  from normalize import normalize_fits
    CLI:      python -m normalize.normalize input.fits output.fits [--source live]

The two programs communicate via the normalized FITS file:
  [normalize] ---(SP_ headers)---> [pipeline]
"""

import argparse
import json
import logging
import os
import sys
import warnings
from dataclasses import dataclass
from pathlib import Path

log = logging.getLogger(__name__)

__version__ = "0.2.0"


@dataclass
class NormalizationResult:
    """Result of FITS metadata normalization."""

    success: bool
    tier: int  # 1=full 2=partial 3=flagged
    resolved: list[str]  # SP_ headers successfully mapped
    unresolved: list[str]  # mandatory SP_ headers missing
    output_path: str | None
    wcs_used: bool = False  # True if WCS was used for RA/Dec
    error: str | None = None

    def to_dict(self) -> dict:
        return {
            "success": self.success,
            "tier": self.tier,
            "resolved": self.resolved,
            "unresolved": self.unresolved,
            "output_path": self.output_path,
            "wcs_used": self.wcs_used,
            "error": self.error,
        }


def _resolve_output_path(output_path: str, base_dir: str | None = None) -> Path:
    """Resolve output under STORAGE_ROOT/base_dir and reject traversal."""
    root_raw = base_dir if base_dir is not None else os.environ.get("STORAGE_ROOT", "").strip()
    path = Path(output_path)
    if ".." in path.parts:
        raise ValueError(f"output_path must not contain '..': {output_path}")
    if not root_raw:
        return path
    root = Path(root_raw).resolve()
    if not path.is_absolute():
        path = root / path
    resolved = path.resolve()
    if root not in resolved.parents and resolved != root:
        raise ValueError(f"output_path outside STORAGE_ROOT/base_dir: {output_path}")
    return resolved


def normalize_fits(
    input_path: str,
    output_path: str,
    source: str = "unknown",
    extra_vocab_path: str | None = None,
    *,
    base_dir: str | None = None,
) -> NormalizationResult:
    """
    Standardize FITS metadata to Saucepan canonical schema.

    Writes SP_-prefixed headers to HDU[0]. Original headers preserved in
    HDU[1] (ORIGHDRS). Does not modify pixel data for science purposes.

    Args:
        input_path:       Path to input FITS file (any source, any instrument)
        output_path:      Path to write normalized FITS
        source:           Data source tag: live | archive | contrib | unknown
        extra_vocab_path: Optional YAML file to extend the synonym vocabulary

    Returns:
        NormalizationResult — tier, resolved/unresolved headers, wcs_used flag
    """
    try:
        out_path = _resolve_output_path(output_path, base_dir=base_dir)
    except ValueError as e:
        return NormalizationResult(
            success=False,
            tier=3,
            resolved=[],
            unresolved=[],
            output_path=None,
            error=str(e),
        )

    try:
        from astropy.io import fits as afits
    except ImportError:
        return NormalizationResult(
            success=False,
            tier=3,
            resolved=[],
            unresolved=[],
            output_path=None,
            error="astropy not installed",
        )

    from normalize.header_map.vocab import apply_vocab, load_vocab
    from normalize.schema import MANDATORY_HEADERS, SP_HEADERS, compute_tier

    warnings.filterwarnings("ignore", message=".*greater than 8 characters.*")
    warnings.filterwarnings("ignore", category=afits.verify.VerifyWarning)

    # --- Open input (memmap avoids eager pixel load) ---
    try:
        hdul = afits.open(input_path, mode="readonly", memmap=True)
    except Exception as e:
        return NormalizationResult(
            success=False,
            tier=3,
            resolved=[],
            unresolved=[],
            output_path=None,
            error=f"Cannot open FITS: {e}",
        )

    primary = hdul[0]
    source_headers = dict(primary.header)

    from grading.fits_limits import (
        FitsSizeLimitError,
        checksum_primary_data,
        ensure_fits_loadable,
    )

    try:
        ensure_fits_loadable(input_path, primary.header)
    except FitsSizeLimitError as exc:
        hdul.close()
        return NormalizationResult(
            success=False,
            tier=3,
            resolved=[],
            unresolved=[],
            output_path=None,
            error=str(exc),
        )

    # --- Checksum original data (chunked; no full-array tobytes) ---
    checksum = checksum_primary_data(primary.data)

    # --- Step 1: synonym vocabulary mapping ---
    extra = extra_vocab_path or os.getenv("SP_VOCAB_PATH")
    vocab = load_vocab(extra)
    resolved = apply_vocab(source_headers, vocab)

    # --- Step 2: WCS coordinate extraction (preferred over header fallback) ---
    wcs_used = False
    if "SP_RA" not in resolved or "SP_DEC" not in resolved:
        ra, dec, wcs_used = _extract_coords_wcs(primary.header, primary.data)
        if ra is not None:
            resolved["SP_RA"] = ra
        if dec is not None:
            resolved["SP_DEC"] = dec

    # --- Step 3: Derived headers (always when inputs exist) ---
    if "SP_DATEOBS" in resolved and "SP_MJD" not in resolved:
        try:
            from astropy.time import Time

            resolved["SP_MJD"] = float(Time(resolved["SP_DATEOBS"]).mjd)
        except Exception as e:
            log.debug("SP_MJD derivation failed: %s", e)

    from normalize.time_headers import derive_time_headers

    resolved.update(derive_time_headers(resolved, source_headers))

    resolved.setdefault("SP_BINX", 1)
    resolved.setdefault("SP_BINY", 1)

    # --- Step 4: Compute pixel scale from optics if not in headers ---
    if "SP_PIXSCALE" not in resolved:
        existing_ps = source_headers.get("SP_PIXSCALE") or source_headers.get(
            "HIERARCH SP_PIXSCALE"
        )
        if existing_ps:
            resolved["SP_PIXSCALE"] = float(existing_ps)
        else:
            ps = _compute_pixel_scale(source_headers)
            if ps is not None:
                resolved["SP_PIXSCALE"] = ps

    # --- Tier ---
    resolved_mandatory = [k for k in MANDATORY_HEADERS if k in resolved]
    tier = compute_tier(len(resolved_mandatory), len(MANDATORY_HEADERS))
    unresolved_mandatory = [k for k in MANDATORY_HEADERS if k not in resolved]

    # --- Build output FITS ---
    out_primary = afits.PrimaryHDU(data=primary.data)

    from normalize.prov_header import build_prov_payload, compact_prov_json

    if os.getenv("METRICS_PROV_URI", "0").lower() in ("1", "true", "yes"):
        prov_json = compact_prov_json(
            build_prov_payload(
                source=source,
                norm_version=__version__,
                checksum=checksum,
                tier=tier,
                wcs_used=wcs_used,
                resolved_keys=list(resolved.keys()),
            )
        )
        resolved["SP_PROV"] = prov_json

    # System SP_ fields
    out_primary.header["SP_NORM"] = (__version__, "Normalization engine version")
    out_primary.header["SP_SRC"] = (source, "Data source: live|archive|contrib|unknown")
    out_primary.header["SP_TIER"] = (tier, "1=full 2=partial 3=flagged")
    out_primary.header["SP_CHKSUM"] = (checksum[:72], "SHA-256 of original data (truncated)")

    # Resolved canonical fields
    for sp_key, value in resolved.items():
        _, description, _ = SP_HEADERS.get(sp_key, ("str", sp_key, False))
        try:
            out_primary.header[sp_key] = (value, description)
        except Exception as e:
            log.debug(f"Could not write {sp_key}={value!r}: {e}")

    # Copy standard WCS keywords from source to HDU[0] so downstream WCS operations work.
    # These are stripped to ORIGHDRS otherwise, breaking reprojection.
    _WCS_KEYS = [
        "CTYPE1",
        "CTYPE2",
        "CRPIX1",
        "CRPIX2",
        "CRVAL1",
        "CRVAL2",
        "CDELT1",
        "CDELT2",
        "CD1_1",
        "CD1_2",
        "CD2_1",
        "CD2_2",
        "PC1_1",
        "PC1_2",
        "PC2_1",
        "PC2_2",
        "LONPOLE",
        "LATPOLE",
        "EQUINOX",
        "RADESYS",
    ]
    for wcs_key in _WCS_KEYS:
        val = source_headers.get(wcs_key)
        if val is not None:
            try:
                out_primary.header[wcs_key] = val
            except Exception:
                pass

    # HDU[1]: original headers preserved verbatim
    orig_table = _make_orighdrs_table(source_headers, afits)
    out_hdul = afits.HDUList([out_primary, orig_table])

    out_path.parent.mkdir(parents=True, exist_ok=True)
    out_hdul.writeto(out_path, overwrite=True)
    hdul.close()

    log.info(f"Normalized {input_path} → {out_path} tier={tier} wcs={wcs_used}")

    return NormalizationResult(
        success=True,
        tier=tier,
        resolved=list(resolved.keys()),
        unresolved=unresolved_mandatory,
        output_path=str(out_path),
        wcs_used=wcs_used,
    )


def _extract_coords_wcs(header, data) -> tuple[float | None, float | None, bool]:
    """Try WCS coordinate extraction. Returns (ra_deg, dec_deg, wcs_used)."""
    try:
        from astropy.wcs import WCS, FITSFixedWarning

        with warnings.catch_warnings():
            warnings.simplefilter("ignore", FITSFixedWarning)
            wcs = WCS(header, naxis=2)

        if not wcs.has_celestial:
            return None, None, False

        # Use image center pixel
        nx = header.get("NAXIS1", data.shape[-1] if data is not None else 100)
        ny = header.get("NAXIS2", data.shape[-2] if data is not None else 100)
        sky = wcs.pixel_to_world(nx / 2, ny / 2)
        return float(sky.ra.deg), float(sky.dec.deg), True

    except Exception as e:
        log.debug(f"WCS extraction failed: {e}")
        return None, None, False


def _compute_pixel_scale(headers: dict) -> float | None:
    """
    Compute pixel scale (arcsec/pixel) from optics specs if available.
    Formula: (pixel_size_um / focal_length_mm) * 206.265
    """
    try:
        # FITS keywords used by various camera/mount software
        pixel_size = (
            headers.get("XPIXSZ")
            or headers.get("PIXSIZE1")
            or headers.get("PIXELSX")
            or headers.get("CCDXPIXE")
        )
        focal_len = (
            headers.get("FOCALLEN")
            or headers.get("FOCAL")
            or headers.get("TELFOCUS")
            or headers.get("FOCL_MM")
        )
        if pixel_size and focal_len:
            return round((float(pixel_size) / float(focal_len)) * 206.265, 4)
    except Exception:
        pass
    # Fallback: compute from WCS CDELT2 (degrees → arcsec)
    try:
        cdelt2 = headers.get("CDELT2") or headers.get("CD2_2")
        if cdelt2:
            return round(abs(float(cdelt2)) * 3600.0, 4)
    except Exception:
        pass
    return None


def _make_orighdrs_table(source_headers: dict, afits) -> object:
    """Build a BinTableHDU preserving all original headers."""
    orig_keys = [str(k) for k in source_headers.keys()]
    orig_vals = [str(v) for v in source_headers.values()]
    key_width = max(1, max((len(value) for value in orig_keys), default=1))
    value_width = max(1, max((len(value) for value in orig_vals), default=1))
    cols = [
        afits.Column(name="KEYWORD", format=f"{key_width}A", array=orig_keys),
        afits.Column(name="VALUE", format=f"{value_width}A", array=orig_vals),
    ]
    tbl = afits.BinTableHDU.from_columns(cols)
    tbl.header["EXTNAME"] = "ORIGHDRS"
    tbl.header["COMMENT"] = "Original FITS headers preserved verbatim"
    return tbl


def main():
    logging.basicConfig(
        level=logging.INFO, stream=sys.stderr, format="%(levelname)s normalize: %(message)s"
    )
    parser = argparse.ArgumentParser(
        description="Program 1: Standardize FITS metadata to Saucepan SP_ schema."
    )
    parser.add_argument("input", help="Input FITS file")
    parser.add_argument("output", help="Output normalized FITS file")
    parser.add_argument(
        "--source", default="unknown", choices=["live", "archive", "contrib", "unknown"]
    )
    parser.add_argument(
        "--vocab", default=None, help="Extra YAML vocabulary file to extend synonyms"
    )
    parser.add_argument(
        "--base-dir", default=None, help="Confinement root for output (default: STORAGE_ROOT)"
    )
    args = parser.parse_args()

    result = normalize_fits(
        args.input, args.output, args.source, args.vocab, base_dir=args.base_dir
    )
    print(json.dumps(result.to_dict(), indent=2))
    sys.exit(0 if result.success else 1)


if __name__ == "__main__":
    main()
