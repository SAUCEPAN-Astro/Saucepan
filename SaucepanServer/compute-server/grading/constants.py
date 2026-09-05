"""
Single source of truth for grading thresholds, weights, and points formula constants.
"""

from __future__ import annotations

GRADER_VERSION = "1.0.0"

# Headline weights for cheap dimensions (deferred dims excluded).
CHEAP_DIMENSION_WEIGHTS: dict[str, float] = {
    "image_quality": 0.40,
    "task_fidelity": 0.35,
    "timeliness": 0.25,
}

# Points formula
BASE_POINTS = 10.0
EXPTIME_CAP_SECONDS = 60.0
TENURE_LOG_SCALE = 0.05

# Stack pre-filter
STACK_ELIGIBLE_MIN_QUALITY = 0.3

# Reputation EMA (Flask ingest / telescope_stats)
RELIABILITY_EMA_ALPHA = 0.15
HEADLINE_EMA_ALPHA = 0.10

# Image quality scoring
SNR_FULL_CREDIT = 50.0
SATURATION_PENALTY_FRACTION = 0.001
IMAGE_QUALITY_WEIGHTS: dict[str, float] = {
    "snr": 0.45,
    "saturation": 0.15,
    "fwhm": 0.40,
}
NEUTRAL_FWHM_SCORE = 0.5
FILTER_ABSENT_SCORE = 0.7
CALIBRATION_BONUS = 0.1
CALIBRATED_STATUSES = frozenset({"B", "D", "F", "BDF", "BD", "BF", "DF"})

# Timeliness scoring (seconds)
CAPTURE_LATENCY_FULL_SEC = 1800.0
CAPTURE_LATENCY_ZERO_SEC = 14400.0
UPLOAD_DURATION_FULL_SEC = 300.0
UPLOAD_DURATION_ZERO_SEC = 3600.0
TIMELINESS_CAPTURE_WEIGHT = 0.6
TIMELINESS_UPLOAD_WEIGHT = 0.4
MISSING_TIMELINESS_SCORE = 0.5

# Stack compatibility (continuous 0–1)
STACK_COMPAT_WEIGHTS: dict[str, float] = {
    "fwhm": 0.40,
    "pixscale": 0.35,
    "noise": 0.25,
}
STACK_COMPAT_NEUTRAL = 0.5
STACK_NOISE_FULL_ADU = 100.0

# Reliability heuristic (alpha)
RELIABILITY_WEIGHTS: dict[str, float] = {
    "calibration": 0.35,
    "plate_solve": 0.35,
    "timeliness": 0.30,
}
RELIABILITY_UNCALIBRATED_SCORE = 0.4
RELIABILITY_NO_PLATE_SCORE = 0.3
