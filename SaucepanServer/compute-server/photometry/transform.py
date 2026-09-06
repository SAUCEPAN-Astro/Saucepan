"""Instrumental → Johnson–Cousins standard-system transform (#419).

Formula (linear AAVSO-style colour term)::

    m_std = m_inst + ZP + T * (CI - CI0) + k * X

where ``T`` is ``color_term``, ``CI0`` is ``color_zero``, ``k`` is
``k_extinction``, and ``X`` is airmass (only when ``airmass_policy`` is
``first_order``). Default policy is ``ignore`` (extinction folded into ZP /
differential reduction).

Profiles live under ``photometry/profiles/*.yaml``.
"""

from __future__ import annotations

import functools
import hashlib
import json
import pathlib
import typing

try:
    import yaml
except ImportError:  # pragma: no cover
    yaml = None  # type: ignore[assignment]

STANDARD_SYSTEM = "johnson_cousins"
PROFILES_DIR = pathlib.Path(__file__).resolve().parent / "profiles"

# Transform coefficient registry (#206). Single source for passband / filter-
# family IDs and AAVSO-style colour/extinction coefficient tables. Lives with
# the other cross-service contracts, not in-tree, so the Go apiserver and the
# SDK can read the same file.
REGISTRY_PATH = (
    pathlib.Path(__file__).resolve().parents[2]
    / "contracts"
    / "transforms"
    / "registry.json"
)


class TransformPathError(ValueError):
    """No registered transform for the requested from_band -> to_band path.

    Raised so callers fail closed: an unregistered bandpass conversion must
    never fall through to an identity / copy-through magnitude.
    """


def _canonical(entry: typing.Mapping[str, typing.Any]) -> bytes:
    """Deterministic JSON encoding of a registry entry, minus its content hash."""
    body = {k: v for k, v in entry.items() if k != "content_hash"}
    return json.dumps(body, sort_keys=True, separators=(",", ":")).encode("utf-8")


def entry_content_hash(entry: typing.Mapping[str, typing.Any]) -> str:
    """sha256 (hex) of a canonicalised registry entry — its pinnable identity."""
    return hashlib.sha256(_canonical(entry)).hexdigest()


@functools.lru_cache(maxsize=8)
def _load_registry_cached(path: str) -> dict[str, typing.Any]:
    raw = json.loads(pathlib.Path(path).read_text(encoding="utf-8"))
    if not isinstance(raw, dict) or "transforms" not in raw:
        raise ValueError(f"transform registry malformed: {path}")
    seen: set[str] = set()
    for e in raw["transforms"]:
        for key in ("id", "from_band", "to_band", "coeffs", "version"):
            if key not in e:
                raise ValueError(f"registry entry {e.get('id')!r} missing {key!r}")
        e["content_hash"] = entry_content_hash(e)
        if e["content_hash"] in seen:
            raise ValueError(f"duplicate registry entry hash for {e['id']!r}")
        seen.add(e["content_hash"])
    return raw


def load_registry(path: str | pathlib.Path | None = None) -> dict[str, typing.Any]:
    """Load the transform registry, annotating each entry with ``content_hash``.

    The result is cached per path; callers must treat it as read-only.
    """
    return _load_registry_cached(str(pathlib.Path(path or REGISTRY_PATH).resolve()))


def registry_passbands(
    *, registry: dict[str, typing.Any] | None = None
) -> dict[str, typing.Any]:
    return dict((registry or load_registry()).get("passbands") or {})


def find_transform(
    from_band: str,
    to_band: str,
    *,
    color_index: str | None = None,
    site: str | None = None,
    registry: dict[str, typing.Any] | None = None,
) -> dict[str, typing.Any]:
    """Return the registry entry for ``from_band -> to_band`` (fail closed).

    ``color_index`` / ``site`` narrow the match when several entries share a
    band pair. Raises :class:`TransformPathError` when nothing matches — the
    caller must then route the frame to a native channel, never coadd blindly.
    """
    reg = registry or load_registry()
    cands = [
        e
        for e in reg["transforms"]
        if e["from_band"] == from_band and e["to_band"] == to_band
    ]
    if color_index is not None:
        cands = [e for e in cands if e.get("color_index") == color_index] or cands
    if site is not None:
        exact = [e for e in cands if e.get("site") == site]
        cands = exact or [e for e in cands if e.get("site") == "generic"] or cands
    if not cands:
        raise TransformPathError(
            f"no registered transform {from_band!r} -> {to_band!r}"
            + (f" (color_index={color_index!r})" if color_index else "")
        )
    return cands[0]


def load_transform_by_hash(
    content_hash: str, *, registry: dict[str, typing.Any] | None = None
) -> dict[str, typing.Any]:
    """Look a transform entry up by its pinned ``content_hash`` (fail closed)."""
    reg = registry or load_registry()
    for e in reg["transforms"]:
        if e["content_hash"] == content_hash:
            return e
    raise TransformPathError(f"no registry entry with content_hash {content_hash!r}")


def apply_transform_coeffs(
    inst_mag: float,
    color: float,
    coeffs: typing.Mapping[str, typing.Any],
    *,
    zp: float = 0.0,
    airmass: float | None = None,
    mag_err: float | None = None,
    color_err: float | None = None,
) -> dict[str, typing.Any]:
    """Apply one registry entry's linear colour term to a single magnitude.

    ``m_std = m_inst + zp + T*(color - CI0) + k*X``  (X only when the entry's
    ``airmass_policy`` is ``first_order``). ``coeffs`` is a registry entry (or
    at least its ``coeffs`` sub-dict + optional ``coeff_err`` / ``airmass_policy``).

    Colour-term uncertainty propagates as
    ``sigma_tr^2 = (color - CI0)^2 * T_err^2 + T^2 * color_err^2`` and is
    combined with ``mag_err`` (when given) into ``std_mag_err``.
    """
    c = dict(coeffs.get("coeffs", coeffs))  # accept a full entry or the sub-dict
    cerr = dict(coeffs.get("coeff_err", {})) if isinstance(coeffs, typing.Mapping) else {}
    policy = str(
        coeffs.get("airmass_policy", "ignore")
        if isinstance(coeffs, typing.Mapping)
        else "ignore"
    ).lower()

    t = float(c.get("T", 0.0))
    ci0 = float(c.get("CI0", 0.0))
    k = float(c.get("k", 0.0) or 0.0)
    t_err = float(cerr.get("T_err", 0.0) or 0.0)
    ci = float(color)

    color_corr = t * (ci - ci0)
    air_corr = 0.0
    if policy == "first_order":
        if airmass is None:
            raise ValueError("airmass required when airmass_policy=first_order")
        air_corr = k * float(airmass)
    elif policy not in {"ignore", "none", "differential"}:
        raise ValueError(f"unknown airmass_policy={policy!r}")

    std_mag = float(inst_mag) + float(zp) + color_corr + air_corr
    out: dict[str, typing.Any] = {
        "std_mag": std_mag,
        "inst_mag": float(inst_mag),
        "zp": float(zp),
        "color": ci,
        "color_corr": color_corr,
        "airmass_corr": air_corr,
        "T": t,
        "CI0": ci0,
        "k": k,
        "from_band": coeffs.get("from_band") if isinstance(coeffs, typing.Mapping) else None,
        "to_band": coeffs.get("to_band") if isinstance(coeffs, typing.Mapping) else None,
        "transform_id": coeffs.get("id") if isinstance(coeffs, typing.Mapping) else None,
        "transform_hash": coeffs.get("content_hash")
        if isinstance(coeffs, typing.Mapping)
        else None,
    }
    if mag_err is not None or color_err is not None:
        d_ci = ci - ci0
        ce = float(color_err or 0.0)
        transform_var = (d_ci * t_err) ** 2 + (t * ce) ** 2
        out["sigma_transform"] = transform_var**0.5
        base = float(mag_err or 0.0)
        out["std_mag_err"] = (base**2 + transform_var) ** 0.5
    return out

_REQUIRED = (
    "profile_id",
    "telescope_id",
    "standard_system",
    "band",
    "color_term",
    "color_zero",
    "zp",
)


def profiles_dir() -> pathlib.Path:
    return PROFILES_DIR


def load_profile(
    name_or_path: str | pathlib.Path,
    *,
    profiles_root: pathlib.Path | None = None,
) -> dict[str, typing.Any]:
    """Load a YAML instrument profile by stem name or absolute/relative path."""
    if yaml is None:
        raise ImportError("PyYAML required to load transform profiles")

    path = pathlib.Path(name_or_path)
    if not path.suffix:
        root = profiles_root or PROFILES_DIR
        path = root / f"{path.name}.yaml"
    elif not path.is_file() and profiles_root is not None:
        path = profiles_root / path.name

    if not path.is_file():
        raise FileNotFoundError(f"transform profile not found: {path}")

    with path.open(encoding="utf-8") as fh:
        raw = yaml.safe_load(fh) or {}
    if not isinstance(raw, dict):
        raise ValueError(f"profile must be a mapping: {path}")

    missing = [k for k in _REQUIRED if k not in raw]
    if missing:
        raise ValueError(f"profile {path} missing keys: {missing}")

    system = str(raw["standard_system"]).strip().lower().replace("-", "_")
    if system not in {"johnson_cousins", "johnson-cousins"}:
        raise ValueError(
            f"unsupported standard_system={raw['standard_system']!r}; "
            f"network SSOT is {STANDARD_SYSTEM} (#419)"
        )
    raw = dict(raw)
    raw["standard_system"] = STANDARD_SYSTEM
    raw.setdefault("color_index", "B-V")
    raw.setdefault("k_extinction", 0.0)
    raw.setdefault("airmass_policy", "ignore")
    return raw


def list_profiles(*, profiles_root: pathlib.Path | None = None) -> list[str]:
    root = profiles_root or PROFILES_DIR
    return sorted(p.stem for p in root.glob("*.yaml"))


def apply_transform(
    inst_mag: float,
    *,
    color_index: float,
    profile: dict[str, typing.Any],
    airmass: float | None = None,
    zp_override: float | None = None,
    mag_err: float | None = None,
    color_term_err: float | None = None,
    color_index_err: float | None = None,
) -> dict[str, typing.Any]:
    """
    Apply ZP + colour term (+ optional first-order extinction) to one magnitude.

    ``zp_override`` lets callers inject per-frame ``SP_ZP`` (#410) without
    mutating the stored profile.

    When ``mag_err`` is set, ``std_mag_err`` propagates colour-term uncertainty::

        σ_std² = σ_mag² + (CI − CI0)² σ_T² + T² σ_CI²

    ``color_term_err`` / ``color_index_err`` default from the profile keys of
    the same names (or ``T_err`` / ``CI_err``). Absent → 0, recovering the
    previous copy-through behaviour.
    """
    zp = float(profile["zp"] if zp_override is None else zp_override)
    t = float(profile["color_term"])
    ci0 = float(profile["color_zero"])
    k = float(profile.get("k_extinction") or 0.0)
    policy = str(profile.get("airmass_policy") or "ignore").lower()
    ci = float(color_index)

    color_corr = t * (ci - ci0)
    air_corr = 0.0
    if policy == "first_order":
        if airmass is None:
            raise ValueError("airmass required when airmass_policy=first_order")
        air_corr = k * float(airmass)
    elif policy not in {"ignore", "none", "differential"}:
        raise ValueError(f"unknown airmass_policy={policy!r}")

    std_mag = float(inst_mag) + zp + color_corr + air_corr

    if color_term_err is None:
        raw_t_err = profile.get("color_term_err", profile.get("T_err"))
        color_term_err = float(raw_t_err) if raw_t_err is not None else 0.0
    else:
        color_term_err = float(color_term_err)
    if color_index_err is None:
        raw_ci_err = profile.get("color_index_err", profile.get("CI_err"))
        color_index_err = float(raw_ci_err) if raw_ci_err is not None else 0.0
    else:
        color_index_err = float(color_index_err)

    out: dict[str, typing.Any] = {
        "std_mag": round(std_mag, 6),
        "inst_mag": float(inst_mag),
        "zp": zp,
        "color_term": t,
        "color_index": ci,
        "color_zero": ci0,
        "color_corr": round(color_corr, 6),
        "color_term_err": color_term_err,
        "color_index_err": color_index_err,
        "airmass_corr": round(air_corr, 6),
        "airmass_policy": policy if policy != "none" else "ignore",
        "airmass": airmass,
        "band": profile.get("band"),
        "standard_system": STANDARD_SYSTEM,
        "telescope_id": profile.get("telescope_id"),
        "profile_id": profile.get("profile_id"),
        "lp.color_term": t,
        "lp.transform_coeff": {
            "T": t,
            "CI0": ci0,
            "k": k,
            "color_index": profile.get("color_index", "B-V"),
            "band": profile.get("band"),
            "T_err": color_term_err,
            "CI_err": color_index_err,
        },
        "lp.transform_applied": True,
        "lp.std_mag": round(std_mag, 6),
    }
    if mag_err is not None:
        # σ²(T·(CI−CI0)) = (CI−CI0)² σ_T² + T² σ_CI²  (CI0 treated as fixed)
        d_ci = ci - ci0
        color_var = (d_ci * color_term_err) ** 2 + (t * color_index_err) ** 2
        std_err = (float(mag_err) ** 2 + color_var) ** 0.5
        out["std_mag_err"] = float(std_err)
        out["lp.mag_err"] = float(std_err)
        out["mag_err_input"] = float(mag_err)
        out["color_corr_err"] = float(color_var**0.5)
    return out


def instrumental_from_truth(
    std_mag: float,
    *,
    color_index: float,
    profile: dict[str, typing.Any],
    airmass: float | None = None,
) -> float:
    """Invert the transform — synthesize instrumental mag for harness fixtures."""
    zp = float(profile["zp"])
    t = float(profile["color_term"])
    ci0 = float(profile["color_zero"])
    k = float(profile.get("k_extinction") or 0.0)
    policy = str(profile.get("airmass_policy") or "ignore").lower()
    color_corr = t * (float(color_index) - ci0)
    air_corr = 0.0
    if policy == "first_order" and airmass is not None:
        air_corr = k * float(airmass)
    return float(std_mag) - zp - color_corr - air_corr
