"""Upload blueprint, constants, and logger."""

import logging
import os

from flask import Blueprint

PRESIGN_EXPIRES_SECONDS = int(os.environ.get("PRESIGN_EXPIRES_SECONDS", "3600"))
MIN_FRAMES_FOR_STACK = 3

uploads_bp = Blueprint("uploads", __name__)
logger = logging.getLogger(__name__)
