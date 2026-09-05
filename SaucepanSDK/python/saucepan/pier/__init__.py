"""On-pier researcher code: the authoring and checking surface.

Researcher code that runs live on a volunteer's pier is authored here, checked
by :mod:`saucepan.pier.check` before it is ever submitted, and (in a later
step) compiled to a wasm32 module the pier runs in a sandbox. Nothing in this
package talks to hardware or the network — it is source tooling.
"""

from saucepan.pier.actions import ACTIONS, DEFAULT_GRANTS
from saucepan.pier.build import CompileError, build
from saucepan.pier.check import CheckResult, Violation, check_source

__all__ = [
    "ACTIONS",
    "DEFAULT_GRANTS",
    "CheckResult",
    "Violation",
    "check_source",
    "build",
    "CompileError",
]
