#!/usr/bin/env python3
"""Checks what devbox's library crates bring into moi (docs/spec/12-moi.md).

Reads `cargo metadata` of tests/compat/moi-consumer, resolved with moi's own direct
dependencies next to devbox's library crates, and fails when:

- the library crates depend, directly or not, on an async runtime, an HTTP crate or a TLS stack;
- a dependency that a library crate and moi both name resolves to two versions (a second major).

Other crates that end up in the graph twice because of devbox are listed, not failed: moi's
own policy warns about those and does not refuse them.
"""

import json
import subprocess
import sys

LIBRARIES = {"devbox-core", "devbox-sync", "devbox-context"}
FORBIDDEN = {
    "an async runtime": {"tokio", "async-std", "smol", "async-executor", "futures-executor"},
    "an HTTP crate": {"hyper", "reqwest", "ureq", "curl", "isahc", "attohttpc", "h2", "http"},
    "a TLS stack": {"rustls", "openssl", "openssl-sys", "native-tls", "boring"},
}


def main() -> int:
    manifest = "tests/compat/moi-consumer/Cargo.toml"
    command = ["cargo", "metadata", "--format-version", "1", "--all-features", "--locked",
               "--manifest-path", manifest]
    metadata = json.loads(subprocess.run(command, check=True, capture_output=True).stdout)
    packages = {package["id"]: package for package in metadata["packages"]}
    nodes = {node["id"]: node for node in metadata["resolve"]["nodes"]}

    def normal(node):
        """The ids a package depends on to be built and run, not to be tested."""
        return [dep["pkg"] for dep in node["deps"]
                if any(kind["kind"] is None for kind in dep["dep_kinds"])]

    def closure(roots):
        seen, todo = set(), list(roots)
        while todo:
            current = todo.pop()
            if current not in seen:
                seen.add(current)
                todo.extend(normal(nodes[current]))
        return seen

    libraries = [id for id, package in packages.items() if package["name"] in LIBRARIES]
    consumer = metadata["resolve"]["root"]
    if len(libraries) != len(LIBRARIES):
        print(f"{manifest} does not depend on all of {sorted(LIBRARIES)}")
        return 1
    brought = closure(libraries)
    problems = []

    for what, names in FORBIDDEN.items():
        for id in sorted(brought):
            if packages[id]["name"] in names:
                problems.append(f"the library crates depend on {what}: {packages[id]['name']}")

    versions = {}
    for id, package in packages.items():
        versions.setdefault(package["name"], set()).add(package["version"])
    moi_direct = {packages[id]["name"] for id in normal(nodes[consumer])} - LIBRARIES
    devbox_direct = {packages[id]["name"] for library in libraries for id in normal(nodes[library])}
    for name in sorted(devbox_direct & moi_direct):
        if len(versions[name]) > 1:
            problems.append(f"{name}: devbox and moi resolve to different versions: "
                            f"{', '.join(sorted(versions[name]))}")

    twice = sorted({packages[id]["name"] for id in brought
                    if len(versions[packages[id]["name"]]) > 1} - (devbox_direct & moi_direct))
    for name in twice:
        print(f"note: {name} is in moi's graph in several versions "
              f"({', '.join(sorted(versions[name]))}), one of them through devbox")

    for problem in problems:
        print(problem)
    if not problems:
        shared = ", ".join(sorted(devbox_direct & moi_direct))
        print(f"ok: {len(brought)} crates through devbox; shared with moi at one version: {shared}")
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
