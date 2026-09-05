"""The fixed v1 on-pier action vocabulary.

Mirror of ``shared/wire/piercode.go`` (``wire.PierCodeActions`` /
``wire.DefaultPierCodeGrants``). Kept in lockstep by hand — the set is tiny and
frozen for v1. Each name is both a campaign-pack grant key and the name of the
function the sandbox injects into researcher code.
"""

from __future__ import annotations

#: read pixels/headers of the just-captured frame (no side effect)
READ_FRAME = "read_frame"
#: post this pier's note to the campaign/task board
BOARD_POST = "board_post"
#: read other piers' board notes
BOARD_READ = "board_read"
#: raise an alert in the researcher's SDK inbox
INBOX_ALERT = "inbox_alert"
#: set a priority-bump / urgency flag on the task
URGENCY_FLAG = "urgency_flag"
#: list the campaign's piers and their online state
LIST_PIERS = "list_piers"
#: request more observing time (== CampaignClient.add_task)
REQUEST_TIME = "request_time"
#: adjust THIS pier's own next exposure within campaign bounds (the only
#: action with a physical effect; never slews, never targets)
NEXT_CAPTURE = "next_capture"

#: v1 menu, in menu order.
ACTIONS: tuple[str, ...] = (
    READ_FRAME,
    BOARD_POST,
    BOARD_READ,
    INBOX_ALERT,
    URGENCY_FLAG,
    LIST_PIERS,
    REQUEST_TIME,
    NEXT_CAPTURE,
)

#: grant map applied when a campaign enables pier_code but names no actions.
DEFAULT_GRANTS: dict[str, bool] = {
    READ_FRAME: True,
    BOARD_POST: True,
    BOARD_READ: True,
}


def is_action(name: str) -> bool:
    """True if *name* is a v1 action."""
    return name in ACTIONS
