"""Flask application factory for the compute-server microservice."""

import logging
import os
import sys

from flask import Flask

logger = logging.getLogger(__name__)


def create_app() -> Flask:
    token = os.environ.get("COMPUTE_TOKEN", "").strip()
    allow_insecure = os.environ.get("COMPUTE_ALLOW_INSECURE", "").strip() == "1"
    if not token and not allow_insecure:
        logger.error("COMPUTE_TOKEN is unset; refusing to start")
        sys.exit(1)
    if not token:
        logger.warning("COMPUTE_ALLOW_INSECURE=1 permits unauthenticated requests")
    from .limits import max_content_length
    from .routes import api_bp

    app = Flask(__name__)
    app.config["MAX_CONTENT_LENGTH"] = max_content_length()
    app.register_blueprint(api_bp)
    return app


if __name__ == "__main__":
    # Local dev runner only — production serves create_app() via gunicorn.
    # Defaults are safe: loopback bind, debugger off. Opt in explicitly for
    # LAN access / the Werkzeug reloader+debugger (never do this on a shared
    # network — the debugger is a remote code-execution console).
    host = os.environ.get("COMPUTE_DEV_HOST", "127.0.0.1")
    port = int(os.environ.get("COMPUTE_DEV_PORT", "5002"))
    debug = os.environ.get("FLASK_DEBUG", "").strip() in ("1", "true", "True")
    create_app().run(host=host, port=port, debug=debug)
