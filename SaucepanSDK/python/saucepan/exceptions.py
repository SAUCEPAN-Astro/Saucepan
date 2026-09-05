class SaucepanError(Exception):
    """Base exception for all Saucepan errors."""

    @property
    def message(self) -> str:
        """Human-readable message (same as str(self))."""
        return str(self)


class AuthError(SaucepanError):
    """Authentication failed (invalid API key, revoked key, etc.)."""

    pass


class ConfigurationError(SaucepanError):
    """Required configuration is missing or malformed (e.g. no task-server URL)."""

    pass


class ValidationError(SaucepanError):
    """Request validation failed."""

    def __init__(self, message: str, fields: dict[str, str] | None = None) -> None:
        super().__init__(message)
        self.fields = fields or {}


class RateLimitError(SaucepanError):
    """Rate limit exceeded."""

    def __init__(self, message: str, retry_after: int | None = None) -> None:
        super().__init__(message)
        self.retry_after = retry_after


class QuotaError(SaucepanError):
    """Quota exceeded."""

    def __init__(
        self, message: str, quota_total: int | None = None, quota_used: int | None = None
    ) -> None:
        super().__init__(message)
        self.quota_total = quota_total
        self.quota_used = quota_used


class NotFoundError(SaucepanError):
    """Resource not found."""

    pass


class ServerError(SaucepanError):
    """Server error (5xx)."""

    def __init__(self, message: str, status_code: int | None = None) -> None:
        super().__init__(message)
        self.status_code = status_code
