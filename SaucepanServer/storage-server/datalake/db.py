"""SQLAlchemy engine and session factory for the catalog database."""

from __future__ import annotations

import logging
import os
import sys
from contextlib import contextmanager
from pathlib import Path
from typing import Generator

from sqlalchemy import create_engine
from sqlalchemy.orm import Session, declarative_base, sessionmaker

logger = logging.getLogger(__name__)

Base = declarative_base()

_engine = None
_SessionLocal = None

# metrics_store.py lives under top-level metrics/python/ (#426 consolidation,
# was co-located here as storage-server/datalake/metrics_store.py). Plug: this
# is a real call site (init_db imports it for the metric_observations table),
# just not one of the ones the move ticket named up front.
_METRICS_PKG = Path(__file__).resolve().parents[3] / "metrics" / "python"
if _METRICS_PKG.is_dir() and str(_METRICS_PKG) not in sys.path:
    sys.path.insert(0, str(_METRICS_PKG))


def get_database_url() -> str:
    return os.environ.get(
        "DATABASE_URL",
        "sqlite:///catalog.db",
    )


def get_engine():
    global _engine, _SessionLocal
    if _engine is not None:
        return _engine

    url = get_database_url()
    connect_args = {}
    if url.startswith("sqlite"):
        connect_args["check_same_thread"] = False
        if url.startswith("sqlite:///") and not url.startswith("sqlite:////"):
            db_path = url.replace("sqlite:///", "", 1)
            if db_path and db_path != ":memory:":
                os.makedirs(os.path.dirname(db_path) or ".", exist_ok=True)

    _engine = create_engine(url, future=True, connect_args=connect_args)
    _SessionLocal = sessionmaker(bind=_engine, autoflush=False, autocommit=False)
    return _engine


def get_session_factory():
    get_engine()
    return _SessionLocal


@contextmanager
def session_scope() -> Generator[Session, None, None]:
    """Provide a transactional scope around a series of operations."""
    factory = get_session_factory()
    session = factory()
    try:
        yield session
        session.commit()
    except Exception:
        session.rollback()
        raise
    finally:
        session.close()


def init_db(app=None) -> None:
    """Create catalog tables if they do not exist."""
    global _engine, _SessionLocal
    if app is not None and app.config.get("DATABASE_URL"):
        os.environ.setdefault("DATABASE_URL", app.config["DATABASE_URL"])
        _engine = None
        _SessionLocal = None

    import catalog  # noqa: F401 — register ORM models on Base.metadata

    try:
        import metrics_store  # noqa: F401 — metric_observations sidecar table
    except ImportError:
        # Non-fatal: catalog init must not depend on the optional metrics
        # sidecar. Previously an unconditional import (would hard-crash
        # init_db if metrics_store.py ever moved) — now a soft/optional
        # dependency, same as the metrics.sidecar plugs elsewhere.
        logger.warning("metrics_store not importable; metric_observations table not registered")

    engine = get_engine()
    Base.metadata.create_all(bind=engine)

    if app is not None:
        app.teardown_appcontext(_shutdown_session)


def _shutdown_session(_exc=None):
    pass
