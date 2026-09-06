"""
Saucepan Data Lake Server

Flask catalog service for the R2 pull-buffer flow: presigned upload, stage
from the object store, post-upload grading, and the L1 frame catalog. The
non-R2 distribution surface (torrent/IPFS seeding, eviction policy engine,
seeder monitor) was removed — see `docs/design/DATALAKE_R2_ONLY.md`.
"""

from flask import Flask, jsonify
import logging
import os
import secrets
from datetime import datetime

# Configure logging
logging.basicConfig(
    level=logging.INFO, format="%(asctime)s - %(name)s - %(levelname)s - %(message)s"
)
logger = logging.getLogger(__name__)


def create_app():
    from auth import ensure_grading_token_at_startup

    # Gunicorn uses create_app() as factory — refuse boot without token outside insecure/dev.
    ensure_grading_token_at_startup()

    secret_key = os.environ.get("SECRET_KEY", "").strip()
    if not secret_key:
        if os.environ.get("DATALAKE_ALLOW_INSECURE") == "1":
            secret_key = f"dev-only-{secrets.token_hex(16)}"
        else:
            raise RuntimeError("SECRET_KEY is required outside DATALAKE_ALLOW_INSECURE=1")

    app = Flask(__name__)

    # Configuration
    app.config.update(
        SECRET_KEY=secret_key,
        DATABASE_URL=os.environ.get("DATABASE_URL", "sqlite:///catalog.db"),
        STORAGE_ROOT=os.environ.get("STORAGE_ROOT", "/data"),
    )

    # Import and register blueprints
    from db import init_db

    init_db(app)

    from routes.upload import uploads_bp

    app.register_blueprint(uploads_bp, url_prefix="/api/v1")

    @app.route("/health")
    def health_check():
        return jsonify(
            {
                "status": "healthy",
                "timestamp": datetime.utcnow().isoformat(),
                "service": "saucepan-data-lake",
            }
        )

    @app.route("/")
    def index():
        return jsonify(
            {
                "service": "Saucepan Data Lake API",
                "version": "1.0.0",
                "endpoints": {
                    "health": "/health",
                    "uploads_presign": "/api/v1/uploads/presign",
                    "uploads_complete": "/api/v1/uploads/complete",
                    "frame_download": "/api/v1/frames/{id}/download",
                    "uploads": "/api/v1/uploads",
                },
            }
        )

    return app


if __name__ == "__main__":
    # Compose uses gunicorn + create_app(). Direct `python app.py` is local-only (#389).
    if os.environ.get("DEV_MODE") != "1" and os.environ.get("DATALAKE_ALLOW_INSECURE") != "1":
        raise SystemExit(
            "Refusing direct app.py boot without DEV_MODE=1 or DATALAKE_ALLOW_INSECURE=1 "
            "(use gunicorn app:create_app() — #389)"
        )
    app = create_app()
    logger.info("Starting Saucepan Data Lake Server (DEV insecure path)")
    app.run(host="127.0.0.1", port=5000, debug=False)
