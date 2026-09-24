#!/usr/bin/env python3
"""Keep E2E coverage inventory aligned with OpenAPI, direct routes, and public RPCs."""

import json
import re
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
MANIFEST = ROOT / "backend/cmd/api/e2e-coverage.json"
OPENAPI = ROOT / "docs/openapi.yaml"
ROUTER = ROOT / "backend/internal/router/server.go"
OIDP = ROOT / "backend/internal/oidp/provider.go"
PUBLIC_PROTO = ROOT / "backend/proto/public/v1"


def direct_routes(path: Path, *, router: bool) -> set[str]:
    routes = set()
    pattern = re.compile(r'^\s*(r|api|authed)\.(Get|Post|Put|Patch|Delete|Options|Head|Handle)\("([^"]+)"')
    for line in path.read_text().splitlines():
        match = pattern.match(line)
        if match is None:
            continue
        receiver, method, route = match.groups()
        if router and receiver in {"api", "authed"}:
            route = "/api/v1" + route
        routes.add(f'{"ANY" if method == "Handle" else method.upper()} {route}')
    return routes


def check_partition(name: str, actual: set[str], covered: dict, unsupported: dict, untested: list) -> list[str]:
    errors = []
    covered_keys = set(covered)
    unsupported_keys = set(unsupported)
    untested_keys = set(untested)
    if len(untested) != len(untested_keys):
        errors.append(f"{name}: duplicate untested entries")
    if covered_keys & untested_keys:
        errors.append(f"{name}: marked both covered and untested: {sorted(covered_keys & untested_keys)}")
    if covered_keys & unsupported_keys or unsupported_keys & untested_keys:
        errors.append(f"{name}: operation appears in multiple statuses")
    inventory = covered_keys | unsupported_keys | untested_keys
    if actual - inventory:
        errors.append(f"{name}: missing inventory entries: {sorted(actual - inventory)}")
    if inventory - actual:
        errors.append(f"{name}: stale inventory entries: {sorted(inventory - actual)}")
    for operation, scenario in {**covered, **unsupported}.items():
        if not isinstance(scenario, str) or not scenario.strip():
            errors.append(f"{name}: {operation} needs a test scenario name")
    return errors


def main() -> int:
    manifest = json.loads(MANIFEST.read_text())
    ids = re.findall(r"^      operationId: (\S+)\s*$", OPENAPI.read_text(), re.MULTILINE)
    if len(ids) != len(set(ids)):
        print("OpenAPI contains duplicate operationId values", file=sys.stderr)
        return 1
    errors = check_partition(
        "OpenAPI", set(ids), manifest["openapi"]["covered"],
        manifest["openapi"].get("unsupported", {}), manifest["openapi"]["untested"]
    )
    route_groups = set(re.findall(r'\.Route\("([^"]+)"', ROUTER.read_text()))
    if route_groups != {"/api/v1"}:
        errors.append(f"direct routes: review route group prefixes: {sorted(route_groups)}")
    routes = direct_routes(ROUTER, router=True) | direct_routes(OIDP, router=False)
    errors += check_partition(
        "direct routes", routes, manifest["directRoutes"]["covered"],
        manifest["directRoutes"].get("unsupported", {}),
        manifest["directRoutes"]["untested"]
    )
    rpcs = set()
    for proto_file in PUBLIC_PROTO.glob("*.proto"):
        service = None
        for line in proto_file.read_text().splitlines():
            service_match = re.match(r"^service (\w+) \{", line)
            if service_match:
                service = service_match.group(1)
            rpc_match = re.match(r"^\s+rpc (\w+)\(", line)
            if rpc_match:
                if service is None:
                    errors.append(f"{proto_file}: RPC outside service")
                else:
                    name = f"{service}/{rpc_match.group(1)}"
                    if name in rpcs:
                        errors.append(f"public gRPC: duplicate RPC {name}")
                    rpcs.add(name)
            if line == "}":
                service = None
    errors += check_partition(
        "public gRPC", rpcs, manifest["publicGrpc"]["covered"],
        manifest["publicGrpc"].get("unsupported", {}),
        manifest["publicGrpc"]["untested"]
    )
    for section in ("openapi", "directRoutes", "publicGrpc"):
        if manifest[section]["untested"]:
            errors.append(
                f"{section}: untested operations remain: {manifest[section]['untested']}"
            )
    scenario_names = set()
    for test_file in (ROOT / "backend/cmd/api").glob("e2e*_test.go"):
        scenario_names.update(re.findall(r'\bt\.Run\(\s*"([^"]+)"', test_file.read_text()))
    covered_scenarios = set(manifest["openapi"]["covered"].values()) | set(
        manifest["directRoutes"]["covered"].values()
    ) | set(manifest["publicGrpc"]["covered"].values()) | set(
        manifest["openapi"].get("unsupported", {}).values()
    )
    if covered_scenarios - scenario_names:
        errors.append(
            f"covered entries lack named E2E subtests: {sorted(covered_scenarios - scenario_names)}"
        )
    if errors:
        print("\n".join(errors), file=sys.stderr)
        return 1
    print(
        f"API E2E inventory current: {len(ids)} OpenAPI operations "
        f"({len(manifest['openapi']['covered'])} covered, "
        f"{len(manifest['openapi'].get('unsupported', {}))} expected 501), {len(routes)} direct routes "
        f"({len(manifest['directRoutes']['covered'])} covered), {len(rpcs)} public RPCs "
        f"({len(manifest['publicGrpc']['covered'])} covered)"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
