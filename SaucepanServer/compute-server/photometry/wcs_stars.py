"""Project campaign comparison stars from sky coordinates onto the frame.

Campaign packs (migration ``011_campaign_comp_stars.sql``, ``campaigns.comp_stars``
JSONB) carry comparison / check stars as RA/Dec. The aperture-photometry code
needs pixel positions on *this* frame. This module is the one bridge: given a
frame WCS it projects each star and flags whether it lands on the sensor.

Legacy fixtures that already carry pixel ``x``/``y`` are passed straight through
(``source="pixel"``), so nothing that worked before breaks.
"""

from __future__ import annotations

import logging
import typing

logger = logging.getLogger(__name__)

try:  # pragma: no cover - import guard
    from astropy.io import fits as _fits
    from astropy.wcs import WCS as _WCS
except ImportError:  # pragma: no cover
    _fits = None  # type: ignore[assignment]
    _WCS = None  # type: ignore[assignment]


def wcs_from_header(header: typing.Any) -> "typing.Any | None":
    """Build an ``astropy.wcs.WCS`` from a FITS header or plain dict.

    Returns ``None`` when astropy is unavailable or the header has no usable
    celestial solution.
    """
    if _WCS is None:
        return None
    try:
        if _fits is not None and isinstance(header, dict):
            header = _fits.Header(
                (str(k), v) for k, v in header.items() if v is not None
            )
        wcs = _WCS(header)
        if not getattr(wcs, "has_celestial", False):
            return None
        return wcs.celestial
    except Exception:  # pragma: no cover - malformed header
        logger.debug("wcs_from_header: no usable WCS", exc_info=True)
        return None


def _radec(raw: dict[str, typing.Any]) -> tuple[float, float] | None:
    ra = raw.get("ra")
    if ra is None:
        ra = raw.get("ra_deg")
    dec = raw.get("dec")
    if dec is None:
        dec = raw.get("dec_deg")
    if ra is None or dec is None:
        return None
    try:
        return float(ra), float(dec)
    except (TypeError, ValueError):
        return None


def _xy(raw: dict[str, typing.Any]) -> tuple[float, float] | None:
    x = raw.get("x")
    if x is None:
        x = raw.get("x_pix")
    y = raw.get("y")
    if y is None:
        y = raw.get("y_pix")
    if x is None or y is None:
        return None
    try:
        return float(x), float(y)
    except (TypeError, ValueError):
        return None


def project_comp_stars(
    stars: typing.Iterable[dict[str, typing.Any]],
    wcs: typing.Any,
    shape: tuple[int, int] | None,
) -> list[dict[str, typing.Any]]:
    """Project comp/check stars onto pixel coordinates.

    ``stars`` items carry at least an ``id`` plus either RA/Dec (``ra``/``dec``)
    or pixel ``x``/``y``. ``wcs`` is an ``astropy.wcs.WCS`` (or ``None`` — then
    only pixel-native stars survive). ``shape`` is ``(ny, nx)`` used for the
    ``in_frame`` test; pass ``None`` to skip bounds checking.

    Each returned dict keeps the input fields and adds ``x``, ``y``,
    ``in_frame`` (bool) and ``source`` (``"wcs"`` / ``"pixel"``). Stars that
    can be placed by neither route are dropped with a debug log.
    """
    ny = nx = None
    if shape is not None and len(shape) >= 2:
        ny, nx = int(shape[0]), int(shape[1])

    out: list[dict[str, typing.Any]] = []
    for raw in stars:
        star = dict(raw)
        radec = _radec(star)
        placed = False

        if radec is not None and wcs is not None:
            try:
                px, py = wcs.all_world2pix(radec[0], radec[1], 0)
                star["x"] = float(px)
                star["y"] = float(py)
                star["source"] = "wcs"
                placed = True
            except Exception:  # pragma: no cover - projection failure
                logger.debug("project_comp_stars: world2pix failed for %s", star.get("id"))

        if not placed:
            xy = _xy(star)
            if xy is not None:
                star["x"], star["y"] = xy
                star["source"] = "pixel"
                placed = True

        if not placed:
            logger.debug(
                "project_comp_stars: star %s has neither WCS nor pixel position",
                star.get("id"),
            )
            continue

        if ny is not None and nx is not None:
            star["in_frame"] = bool(0.0 <= star["x"] <= nx - 1 and 0.0 <= star["y"] <= ny - 1)
        else:
            star["in_frame"] = True
        out.append(star)

    return out
