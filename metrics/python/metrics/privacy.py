"""Privacy filters for persisted or emitted metrics context."""

from __future__ import annotations

from typing import Any


_PRIVATE_CONTEXT_KEYS = frozenset(
    {
        "observer",
        "user",
        "identity",
        "user_id",
        "userid",
        "username",
        "researcher",
        "researcher_id",
        "display_name",
        "operator",
        "owner",
        "contact",
        "author",
        "account",
        "principal",
    }
)
_PRIVATE_CONTEXT_KEY_COMPACT = frozenset(
    name.replace("_", "") for name in _PRIVATE_CONTEXT_KEYS
)


def _private_context_key(key: object) -> bool:
    compact = str(key).strip().lower().replace("-", "").replace("_", "")
    return (
        compact in _PRIVATE_CONTEXT_KEY_COMPACT
        or "email" in compact
        or compact.endswith(
            (
                "observer",
                "username",
                "userid",
                "displayname",
                "researchername",
                "operatorname",
                "ownername",
                "contactname",
                "authorname",
                "name",
                "path",
                "paths",
                "directory",
                "dir",
            )
        )
    )


def _sanitize_context_value(value: Any) -> Any:
    if isinstance(value, dict):
        return {
            key: _sanitize_context_value(nested)
            for key, nested in value.items()
            if not _private_context_key(key)
        }
    if isinstance(value, list):
        return [_sanitize_context_value(item) for item in value]
    return value


def sanitize_context(value: Any) -> dict[str, Any]:
    """Keep a metrics context machine-scoped and free of local paths."""
    sanitized = _sanitize_context_value(value or {})
    return sanitized if isinstance(sanitized, dict) else {}


def sanitize_observation(value: Any) -> dict[str, Any]:
    """Remove identity and local-path fields from an observation envelope."""
    if not isinstance(value, dict):
        return {}
    observation = dict(value)
    observation["context"] = sanitize_context(value.get("context"))
    metrics = value.get("metrics")
    if isinstance(metrics, dict):
        safe_metric_names = {"frame.target_name", "task.name"}
        observation["metrics"] = {
            key: (
                "error"
                if "error" in str(key).strip().lower().replace("_", "")
                else _sanitize_context_value(metric)
            )
            for key, metric in metrics.items()
            if key in safe_metric_names or not _private_context_key(key)
        }
    return observation
