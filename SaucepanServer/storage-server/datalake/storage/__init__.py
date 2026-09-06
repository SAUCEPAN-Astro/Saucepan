"""Local disk policy + swappable object storage backends."""

from storage.factory import get_storage_backend, reset_storage_backend

__all__ = ["get_storage_backend", "reset_storage_backend"]
