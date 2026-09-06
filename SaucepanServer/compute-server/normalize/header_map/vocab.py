"""
vocab.py — synonym-based header mapper.

Replaces instrument-specific rules with a single concept vocabulary.
Any camera whose headers contain a known synonym gets mapped automatically.
To support a new camera: add its key names to synonyms.yaml — no new rule blocks.
"""

import logging
import os

import yaml

log = logging.getLogger(__name__)

_SYNONYMS_PATH = os.path.join(os.path.dirname(__file__), "synonyms.yaml")


def load_vocab(extra_path: str | None = None) -> dict:
    """Load the synonym vocabulary. Optionally merge a user-supplied YAML."""
    with open(_SYNONYMS_PATH) as f:
        vocab = yaml.safe_load(f)
    if extra_path and os.path.exists(extra_path):
        with open(extra_path) as f:
            extra = yaml.safe_load(f)
        for sp_key, cfg in extra.items():
            if sp_key in vocab:
                vocab[sp_key]["synonyms"] = list(
                    dict.fromkeys(vocab[sp_key]["synonyms"] + cfg.get("synonyms", []))
                )
            else:
                vocab[sp_key] = cfg
    return vocab


# Physical-domain validators: drop a mapping if the resolved value is out of bounds.
# This prevents synonym false-positives from writing nonsense SP_ values silently.
_VALIDATORS: dict = {
    "SP_RA": lambda v: 0.0 <= float(v) <= 360.0,
    "SP_DEC": lambda v: -90.0 <= float(v) <= 90.0,
    "SP_EXPTIME": lambda v: 0.0 < float(v) < 86400.0,
    "SP_GAIN": lambda v: 0.0 < float(v) < 100000.0,
    "SP_PIXSCALE": lambda v: 0.01 < float(v) < 3600.0,
    "SP_FWHM": lambda v: 0.01 < float(v) < 3600.0,
    "SP_SITELAT": lambda v: -90.0 <= float(v) <= 90.0,
    "SP_SITELON": lambda v: -180.0 <= float(v) <= 180.0,
    "SP_SITEELV": lambda v: -500.0 <= float(v) <= 9000.0,
}


def apply_vocab(source_headers: dict, vocab: dict) -> dict:
    """
    Map source headers → SP_ canonical headers using synonym lookup.

    First match in synonyms list wins. No instrument matching required.
    WCS coordinate extraction is attempted separately in normalize.py.
    Domain validators drop mappings that produce physically impossible values.
    """
    resolved = {}
    for sp_key, cfg in vocab.items():
        synonyms = cfg.get("synonyms", [])
        transform_name = cfg.get("transform")
        for src_key in synonyms:
            if src_key in source_headers:
                raw = source_headers[src_key]
                try:
                    value = _apply_transform(raw, transform_name)
                except Exception as e:
                    log.debug(f"{src_key}→{sp_key} transform '{transform_name}' failed: {e}")
                    continue
                # Validate against physical bounds — drop mapping if out of range
                validator = _VALIDATORS.get(sp_key)
                if validator is not None:
                    try:
                        if not validator(value):
                            log.warning(
                                f"Dropped {sp_key}: '{src_key}'={value!r} failed domain validation"
                            )
                            continue
                    except Exception:
                        log.debug(f"Validator for {sp_key} raised on {value!r}, skipping")
                        continue
                resolved[sp_key] = value
                break
    return resolved


# ---------------------------------------------------------------------------
# Value transforms — normalize raw values for heterogeneous stacking
# ---------------------------------------------------------------------------


def _apply_transform(value, transform: str | None):
    if transform is None:
        return value
    if transform == "float":
        return float(value)
    if transform == "int":
        return int(value)
    if transform == "str":
        return str(value).strip()
    if transform == "utc_iso":
        return _to_utc_iso(value)
    if transform == "ra_deg":
        return _to_ra_deg(value)
    if transform == "dec_deg":
        return _to_dec_deg(value)
    if transform == "filter_canon":
        return _canonicalize_filter(str(value).strip())
    return value


def _to_utc_iso(value) -> str:
    """Any date/time format → ISO 8601 UTC string."""
    from astropy.time import Time

    s = str(value).strip()
    # MJD
    try:
        f = float(s)
        if f > 2400000:  # JD range
            return Time(f, format="jd").isot + "Z"
        if f > 40000:  # MJD range
            return Time(f, format="mjd").isot + "Z"
    except ValueError:
        pass
    return Time(s).isot + "Z"


def _to_ra_deg(value) -> float:
    """Decimal degrees or sexagesimal string → decimal degrees RA."""
    s = str(value).strip()
    if ":" in s or " " in s.strip():
        # sexagesimal — astropy handles "HH MM SS.s" and "HH:MM:SS.s"
        import astropy.units as u
        from astropy.coordinates import Angle

        return Angle(s, unit=u.hourangle).deg
    return float(s)


def _to_dec_deg(value) -> float:
    """Decimal degrees or sexagesimal string → decimal degrees Dec."""
    s = str(value).strip()
    if ":" in s or ((" " in s) and (s.count(".") <= 1)):
        import astropy.units as u
        from astropy.coordinates import Angle

        return Angle(s, unit=u.deg).deg
    return float(s)


# Canonical filter name map — normalizes aliases to a standard name
_FILTER_ALIASES: dict[str, str] = {
    # Hydrogen-alpha
    "h-alpha": "Ha",
    "halpha": "Ha",
    "h_alpha": "Ha",
    "ha": "Ha",
    "656": "Ha",
    "6563": "Ha",
    # OIII
    "o3": "OIII",
    "oiii": "OIII",
    "o-iii": "OIII",
    "5007": "OIII",
    # SII
    "s2": "SII",
    "sii": "SII",
    "s-ii": "SII",
    "6717": "SII",
    # Hbeta
    "hb": "Hb",
    "hbeta": "Hb",
    "h-beta": "Hb",
    "4861": "Hb",
    # Broadband
    "luminance": "L",
    "lum": "L",
    "clear": "L",
    "none": "L",
    "red": "R",
    "green": "G",
    "blue": "B",
    "osc": "OSC",
    "bayer": "OSC",
    "rgb": "RGB",
    "colour": "RGB",
    "color": "RGB",
    # SDSS / photometric
    "u'": "u",
    "g'": "g",
    "r'": "r",
    "i'": "i",
    "z'": "z",
}


def _canonicalize_filter(raw: str) -> str:
    key = raw.lower().strip()
    return _FILTER_ALIASES.get(key, raw)
