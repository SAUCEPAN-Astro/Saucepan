"""Small validation helpers for values placed into URL path segments."""

from __future__ import annotations

import re

_SAFE_SEGMENT = re.compile(r"[A-Za-z0-9][A-Za-z0-9._~-]{0,127}\Z")


def path_segment(value: object, *, name: str = "identifier") -> str:
    """Return a safe opaque identifier for interpolation into a URL path."""
    text = str(value)
    if not _SAFE_SEGMENT.fullmatch(text):
        raise ValueError(f"{name} contains invalid path characters")
    return text
