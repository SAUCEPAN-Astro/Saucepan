#!/usr/bin/env python3
"""Regenerate routes.json from the apiRoutes table in cmd/apiserver/routes.go.

routes.json is the source of truth for the apiserver HTTP contract; this
script just keeps its serialised form in sync with the Go table. Run from the
task-server module root:

    python3 contracts/rest/gen.py

rest_contract_test.go fails the build if routes.json and the Go table disagree,
so a stale routes.json is caught in CI, not silently shipped.
"""

from __future__ import annotations

import json
import re
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]  # task-server/
ROUTES_GO = ROOT / "cmd" / "apiserver" / "routes.go"
OUT = ROOT / "contracts" / "rest" / "routes.json"

_SURFACE = {
    "surfacePier": "pier",
    "surfaceAuth": "auth",
    "surfaceDeveloper": "developer",
    "surfaceResearcher": "researcher",
    "surfaceInfra": "infra",
}
_ROW = re.compile(
    r'\{"(GET|POST|PATCH|DELETE|PUT)",\s*"([^"]+)",\s*'
    r"(surfacePier|surfaceAuth|surfaceDeveloper|surfaceResearcher|surfaceInfra),"
)


def main() -> None:
    src = ROUTES_GO.read_text()
    routes = [
        {"method": m, "path": p, "surface": _SURFACE[s]}
        for m, p, s in _ROW.findall(src)
    ]
    # The two health endpoints are wired directly in registerAPIRoutes.
    routes += [
        {"method": "GET", "path": "/", "surface": "infra"},
        {"method": "GET", "path": "/cohort/status", "surface": "infra"},
    ]
    routes.sort(key=lambda r: (r["path"], r["method"]))
    doc = {
        "_comment": (
            "Source of truth for the apiserver HTTP contract. "
            "Regenerate with contracts/rest/gen.py after editing "
            "cmd/apiserver/routes.go. rest_contract_test.go and the SDK's "
            "tests/test_rest_contract.py both gate against this file."
        ),
        "routes": routes,
    }
    OUT.write_text(json.dumps(doc, indent=2) + "\n")
    print(f"wrote {len(routes)} routes to {OUT.relative_to(ROOT)}")


if __name__ == "__main__":
    main()
