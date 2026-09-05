"""Validate FITS download URLs with a positive allowlist."""

from __future__ import annotations

import ipaddress
import logging
import os
from urllib.parse import urlparse

from saucepan.exceptions import ConfigurationError

logger = logging.getLogger(__name__)

_R2_HOST_SUFFIX = ".r2.cloudflarestorage.com"
_METADATA_HOSTS = frozenset({"169.254.169.254", "metadata.google.internal"})
_LOOPBACK_HOSTS = frozenset({"localhost", "127.0.0.1", "::1"})


class LandingURLRejected(ValueError):  # noqa: N818 - retained public exception name
    """FITS download URL failed landing host policy."""


def _task_server_hostnames() -> frozenset[str]:
    """Hosts that must never serve FITS bytes: the task server itself.

    No host is baked in. The denied host is taken from ``SAUCEPAN_TASK_URL``
    (the task-server base URL — its hostname is extracted). When it is not set,
    the set is empty; the positive allowlist in :func:`validate_landing_url` is
    still the fail-closed layer, so an unset task URL never widens what passes.

    Raises:
        ConfigurationError: ``SAUCEPAN_TASK_URL`` is set but has no host.
    """
    hosts: set[str] = set()

    task_url = os.environ.get("SAUCEPAN_TASK_URL", "").strip()
    if task_url:
        candidate = task_url if "://" in task_url else f"https://{task_url}"
        try:
            host = (urlparse(candidate).hostname or "").lower()
        except ValueError:
            host = ""
        if not host:
            raise ConfigurationError(
                "SAUCEPAN_TASK_URL has no host — set it to the "
                "task-server base URL, e.g. https://task.example.net:8080"
            )
        hosts.add(host)

    return frozenset(hosts)


def _env_allowlist_hosts() -> frozenset[str]:
    raw = os.environ.get("SAUCEPAN_LANDING_HOST_ALLOWLIST", "")
    return frozenset(part.strip().lower() for part in raw.split(",") if part.strip())


def _r2_public_endpoint_host() -> str | None:
    raw = os.environ.get("R2_PUBLIC_ENDPOINT", "").strip()
    if not raw:
        return None
    if "://" not in raw:
        raw = f"https://{raw}"
    return urlparse(raw).hostname.lower() if urlparse(raw).hostname else None


def _unsafe_dev_enabled() -> bool:
    return os.environ.get("SAUCEPAN_LANDING_ALLOW_UNSAFE", "").strip() == "1"


def _is_private_or_link_local(ip: ipaddress.IPv4Address | ipaddress.IPv6Address) -> bool:
    if ip.is_loopback:
        return True
    if ip.is_private or ip.is_link_local:
        return True
    if isinstance(ip, ipaddress.IPv4Address):
        # 100.64.0.0/10 CGNAT
        return ip.packed[0] == 100 and (ip.packed[1] & 0xC0) == 0x40
    return False


def _host_is_on_allowlist(host: str) -> bool:
    if host.endswith(_R2_HOST_SUFFIX):
        return True
    r2_public = _r2_public_endpoint_host()
    if r2_public and host == r2_public:
        return True
    return host in _env_allowlist_hosts()


def validate_landing_url(url: str) -> str:
    """
    Return *url* if its host matches the landing allowlist.

    Allowed by default: ``*.r2.cloudflarestorage.com``, ``R2_PUBLIC_ENDPOINT`` host,
    and comma-separated ``SAUCEPAN_LANDING_HOST_ALLOWLIST``.

    The task-server host (from ``SAUCEPAN_TASK_URL``) and cloud metadata
    endpoints are always rejected. No
    task host is baked in; when neither is set the positive allowlist above is
    still the fail-closed layer. Set ``SAUCEPAN_LANDING_ALLOW_UNSAFE=1`` only for
    local dev (e.g. MinIO on loopback/private LAN); this permits ``http`` and
    private hosts with a warning.

    Raises:
        LandingURLRejected: URL host is not permitted by the policy.
        ConfigurationError: ``SAUCEPAN_TASK_URL`` is set but has no host.
    """
    trimmed = url.strip()
    if not trimmed:
        raise LandingURLRejected("landing URL is empty")

    parsed = urlparse(trimmed)
    if not parsed.scheme or not parsed.netloc:
        raise LandingURLRejected("invalid landing URL")

    host = (parsed.hostname or "").lower()
    if not host:
        raise LandingURLRejected("landing URL missing host")

    if host in _METADATA_HOSTS:
        raise LandingURLRejected(f"cloud metadata endpoint blocked: {host}")

    if host in _task_server_hostnames():
        raise LandingURLRejected(
            f"FITS URL host {host!r} is the task server — bytes must not transit it"
        )

    unsafe = _unsafe_dev_enabled()
    scheme = parsed.scheme.lower()
    if scheme != "https":
        if unsafe and scheme == "http":
            logger.warning("SAUCEPAN_LANDING_ALLOW_UNSAFE=1: allowing HTTP landing URL (dev only)")
        else:
            raise LandingURLRejected(
                f"landing URL scheme {scheme!r} not allowed (https only; "
                "set SAUCEPAN_LANDING_ALLOW_UNSAFE=1 for local http dev)"
            )

    if _host_is_on_allowlist(host):
        return trimmed

    if unsafe:
        if host in _LOOPBACK_HOSTS:
            logger.warning(
                "SAUCEPAN_LANDING_ALLOW_UNSAFE=1: allowing loopback landing URL %s",
                host,
            )
            return trimmed
        try:
            ip = ipaddress.ip_address(host)
        except ValueError:
            ip = None
        if ip is not None and _is_private_or_link_local(ip):
            logger.warning(
                "SAUCEPAN_LANDING_ALLOW_UNSAFE=1: allowing private/link-local landing URL %s",
                host,
            )
            return trimmed

    raise LandingURLRejected(
        f"landing URL host {host!r} is not on allowlist "
        "(expected R2 / SAUCEPAN_LANDING_HOST_ALLOWLIST; "
        "set SAUCEPAN_LANDING_ALLOW_UNSAFE=1 for local dev only)"
    )
