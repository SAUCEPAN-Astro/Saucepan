"""REST contract gate, SDK side.

Every HTTP path the SDK calls must be a real, registered apiserver route. The
route set is single-sourced in
``SaucepanServer/task-server/contracts/rest/routes.json`` (a Go test keeps that
file equal to the apiserver's route table); this test loads the same file and
checks every path literal in the client modules against it. A path typo, a
renamed endpoint, or a route removed server-side all fail here instead of at a
researcher's runtime.

The published developer API additionally has ``SaucepanSDK/openapi.yaml``; a Go
test gates that spec against the route table. Here we only sanity-check the
spec parses and its paths are a subset of the contract.
"""

from __future__ import annotations

import json
import re
from pathlib import Path

import yaml

_SDK = Path(__file__).resolve().parents[1] / "saucepan"
_REPO = Path(__file__).resolve().parents[3]
_ROUTES_JSON = _REPO / "SaucepanServer" / "task-server" / "contracts" / "rest" / "routes.json"
_OPENAPI = _REPO / "SaucepanSDK" / "openapi.yaml"

# _request("METHOD", "PATH" | f"PATH", ...) — \s* spans newlines so the
# method-on-its-own-line call style in campaigns.py is covered too.
_REQUEST_CALL = re.compile(
    r"""_request\(\s*["'](GET|POST|PUT|PATCH|DELETE)["']\s*,\s*f?["']([^"']+)["']""",
    re.DOTALL,
)

# QuestClient predates CampaignClient and calls requests directly. Keep this
# separate from _REQUEST_CALL because its URL is built from self.base_url.
_QUEST_REQUEST_CALL = re.compile(
    r"""requests\.((?i:GET|POST|PUT|PATCH|DELETE))\(\s*f["']\{self\.base_url\}([^"']+)["']""",
    re.DOTALL,
)


def _norm(path: str) -> str:
    """Collapse every {param} / f-string {expr} segment to {}."""
    return re.sub(r"\{[^}]+\}", "{}", path)


def _contract_routes() -> set[tuple[str, str]]:
    doc = json.loads(_ROUTES_JSON.read_text())
    return {(r["method"], _norm(r["path"])) for r in doc["routes"]}


def _sdk_calls() -> list[tuple[str, str, str]]:
    """(method, normalised_path, source_file) for every SDK API call."""
    out: list[tuple[str, str, str]] = []
    for mod in ("_http.py", "campaigns.py", "messageboard.py", "campaign_inbox.py", "quest.py"):
        src = (_SDK / mod).read_text()
        for method, raw in _REQUEST_CALL.findall(src):
            # _http.py's Client is constructed with a base_url that already
            # ends in /api/v1; its call sites pass the sub-path only.
            candidates = [raw]
            if mod == "_http.py" and not raw.startswith("/api/v1"):
                candidates = ["/api/v1" + raw]
            # TextInbox builds paths from self._channel ∈ {alerts, updates}.
            expanded: list[str] = []
            for c in candidates:
                if "{self._channel}" in c:
                    expanded += [
                        c.replace("{self._channel}", "alerts"),
                        c.replace("{self._channel}", "updates"),
                    ]
                else:
                    expanded.append(c)
            for c in expanded:
                out.append((method, _norm(c), mod))

        if mod == "quest.py":
            for method, raw in _QUEST_REQUEST_CALL.findall(src):
                out.append((method.upper(), _norm(raw), mod))
    return out


def test_routes_json_present_and_shaped():
    assert _ROUTES_JSON.is_file(), f"missing {_ROUTES_JSON}"
    doc = json.loads(_ROUTES_JSON.read_text())
    assert doc["routes"], "routes.json has no routes"
    seen = set()
    valid_methods = {"GET", "POST", "PUT", "PATCH", "DELETE"}
    valid_surfaces = {"pier", "auth", "developer", "researcher", "infra"}
    for r in doc["routes"]:
        assert set(r) == {"method", "path", "surface"}
        assert r["method"] in valid_methods
        assert r["path"].startswith("/") and " " not in r["path"]
        assert r["surface"] in valid_surfaces
        key = (r["method"], _norm(r["path"]))
        assert key not in seen, f"duplicate route in routes.json: {r['method']} {r['path']}"
        seen.add(key)


def test_every_sdk_request_path_is_a_registered_route():
    contract = _contract_routes()
    calls = _sdk_calls()
    assert calls, "no SDK API call sites found — regex likely broke"

    unknown = sorted(
        {f"{m} {p}  (from {src})" for m, p, src in calls if (m, p) not in contract}
    )
    assert not unknown, "SDK calls paths with no matching apiserver route:\n  " + "\n  ".join(
        unknown
    )


def test_raw_auth_endpoints_exist():
    # campaigns.py hits these directly with requests.post, not via _request().
    contract = _contract_routes()
    src = (_SDK / "campaigns.py").read_text()
    for path in ("/auth/login", "/auth/refresh"):
        assert path in src, f"expected {path} in campaigns.py"
        assert ("POST", path) in contract, f"{path} is not a registered route"


def test_openapi_parses_and_is_subset_of_contract():
    assert _OPENAPI.is_file(), f"required OpenAPI spec missing: {_OPENAPI}"
    spec = yaml.safe_load(_OPENAPI.read_text())
    assert spec.get("openapi", "").startswith("3."), "not an OpenAPI 3.x doc"
    assert spec.get("paths"), "openapi.yaml declares no paths"

    contract = _contract_routes()
    http_methods = {"get", "post", "put", "patch", "delete"}
    missing = []
    for path, ops in spec["paths"].items():
        methods = {m for m in ops if m.lower() in http_methods}
        assert methods, f"{path} declares no HTTP method"
        for m in methods:
            method = m.upper()
            np = _norm(path)
            if (method, np) in contract or (method, "/api/v1" + np) in contract:
                continue
            missing.append(f"{method} {path}")
    assert not missing, (
        "openapi.yaml declares paths absent from the route contract:\n  "
        + "\n  ".join(sorted(missing))
    )
