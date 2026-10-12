#!/usr/bin/env python3
"""Workflow action SHA-pin gate (#1782).

`adapter-canary.yml` merged pinned to an `actions/setup-node` commit that does
not exist. Its jobs skip on pull requests, so the pin first ran on `main`, and
both canary legs failed at setup. Nothing checked the pin: actionlint never
asks GitHub whether a SHA exists, and Dependabot proposes bumps without
auditing the refs already committed.

This gate reads every `uses: owner/repo[/path]@<ref>` in `.github/workflows/`
and `.github/actions/`, and asks the GitHub API whether each distinct
`owner/repo@sha` resolves. A ref that is not a 40-hex commit SHA fails too: a
tag or branch can be moved, so it is not a pin. `./local` actions and
`docker://` images are not GitHub refs and are skipped.

Bounded and cached:
  * each distinct pin is one request (8 at a time, 10-second timeout, one
    retry on a network error or a 5xx);
  * a pin that resolved is cached, keyed by `owner/repo@sha` -- a commit SHA
    names immutable content -- in `$NIGHTGAUGE_ACTION_PIN_CACHE`, default
    `<git common dir>/nightgauge-action-pins.json`, which every worktree of the
    clone shares. A failure is never cached.

Authentication: `GITHUB_TOKEN`, then `GH_TOKEN`, then `gh auth token`, then
none (60 requests an hour). `GITHUB_API_URL` overrides the API root.

Offline and rate-limited: a pin that is already cached needs no request, so a
warm cache checks offline. A pin that cannot be asked about (no network,
timeout, 5xx after the retry, rate limit, a token GitHub refuses) is neither a
pass nor a broken pin: the gate prints a `HARNESS ERROR` line, which
`scripts/ci-local.sh` reports as an INFRASTRUCTURE error, and exits 2.

Exit codes:
  0  every pin resolves
  1  a pin does not resolve, or a `uses:` ref is not a commit SHA
  2  a pin could not be checked (see above)
"""

from __future__ import annotations

import concurrent.futures
import json
import os
import re
import subprocess
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path

SCAN_DIRS = (Path(".github/workflows"), Path(".github/actions"))
USES = re.compile(r"""^\s*(?:-\s+)?uses:\s*["']?([^\s"'#]+)""")
SHA = re.compile(r"^[0-9a-f]{40}$")
TIMEOUT = 10
WORKERS = 8


def scan(root: Path) -> dict[str, list[str]]:
    """Map each `owner/repo@ref` to the `file:line` sites that use it."""
    sites: dict[str, list[str]] = {}
    for d in SCAN_DIRS:
        base = root / d
        if not base.is_dir():
            continue
        for path in sorted(base.rglob("*")):
            if path.suffix not in (".yml", ".yaml") or not path.is_file():
                continue
            rel = path.relative_to(root)
            for n, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
                m = USES.match(line)
                if not m:
                    continue
                ref = m.group(1)
                if ref.startswith("./") or ref.startswith("docker://"):
                    continue
                target, _, version = ref.partition("@")
                parts = target.split("/")
                key = f"{'/'.join(parts[:2])}@{version}" if len(parts) >= 2 and version else ref
                sites.setdefault(key, []).append(f"{rel}:{n}")
    return sites


def cache_path(root: Path) -> Path:
    override = os.environ.get("NIGHTGAUGE_ACTION_PIN_CACHE")
    if override:
        return Path(override)
    try:
        common = subprocess.run(
            ["git", "rev-parse", "--path-format=absolute", "--git-common-dir"],
            cwd=root, capture_output=True, text=True, check=True, timeout=10,
        ).stdout.strip()
        return Path(common) / "nightgauge-action-pins.json"
    except (OSError, subprocess.SubprocessError):
        return root / ".git" / "nightgauge-action-pins.json"


def load_cache(path: Path) -> set[str]:
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, ValueError):
        return set()
    return {k for k in data.get("resolved", []) if isinstance(k, str)}


def save_cache(path: Path, resolved: set[str]) -> None:
    try:
        path.parent.mkdir(parents=True, exist_ok=True)
        tmp = path.with_name(path.name + f".{os.getpid()}.tmp")
        tmp.write_text(json.dumps({"resolved": sorted(resolved)}, indent=1) + "\n", encoding="utf-8")
        os.replace(tmp, path)
    except OSError as exc:  # a cache that cannot be written only costs requests
        print(f"note: action-pin cache not written ({path}): {exc}", file=sys.stderr)


def token() -> str:
    for name in ("GITHUB_TOKEN", "GH_TOKEN"):
        if os.environ.get(name):
            return os.environ[name]
    try:
        out = subprocess.run(["gh", "auth", "token"], capture_output=True, text=True, timeout=10)
        return out.stdout.strip() if out.returncode == 0 else ""
    except (OSError, subprocess.SubprocessError):
        return ""


def resolve(api: str, auth: str, pin: str) -> tuple[str, str]:
    """Return ("ok" | "missing" | "infra", detail) for one owner/repo@sha."""
    repo, _, sha = pin.partition("@")
    req = urllib.request.Request(f"{api}/repos/{repo}/commits/{sha}", method="GET")
    req.add_header("Accept", "application/vnd.github+json")
    req.add_header("X-GitHub-Api-Version", "2022-11-28")
    if auth:
        req.add_header("Authorization", f"Bearer {auth}")
    detail = ""
    for attempt in range(2):
        try:
            with urllib.request.urlopen(req, timeout=TIMEOUT) as resp:
                resp.read()
                return "ok", ""
        except urllib.error.HTTPError as exc:
            if exc.code in (404, 422):
                return "missing", f"HTTP {exc.code}"
            remaining = exc.headers.get("x-ratelimit-remaining") if exc.headers else None
            if exc.code == 429 or (exc.code == 403 and remaining == "0"):
                return "infra", f"rate limited (HTTP {exc.code})"
            if exc.code in (401, 403):
                return "infra", f"HTTP {exc.code}: the API refused the request's credentials"
            detail = f"HTTP {exc.code}"
        except (urllib.error.URLError, TimeoutError, OSError) as exc:
            detail = f"no answer: {getattr(exc, 'reason', exc)}"
        if attempt == 0:
            time.sleep(1)
    return "infra", detail


def main() -> int:
    root = Path(sys.argv[1]) if len(sys.argv) > 1 else Path.cwd()
    sites = scan(root)
    unpinned = sorted(k for k in sites if not SHA.match(k.partition("@")[2]))
    pins = sorted(k for k in sites if k not in unpinned)

    cpath = cache_path(root)
    cached = load_cache(cpath)
    todo = [p for p in pins if p not in cached]
    api = os.environ.get("GITHUB_API_URL", "https://api.github.com").rstrip("/")
    auth = token() if todo else ""

    results: dict[str, tuple[str, str]] = {}
    if todo:
        with concurrent.futures.ThreadPoolExecutor(max_workers=WORKERS) as pool:
            for pin, res in zip(todo, pool.map(lambda p: resolve(api, auth, p), todo)):
                results[pin] = res

    newly = {p for p, (state, _) in results.items() if state == "ok"}
    if newly:
        save_cache(cpath, cached | newly)

    failed = False
    for key in unpinned:
        failed = True
        for site in sites[key]:
            print(f"✗ {site}: uses {key}, which is not pinned to a 40-hex commit SHA")
    for pin in pins:
        state, detail = results.get(pin, ("ok", ""))
        if state == "missing":
            failed = True
            for site in sites[pin]:
                print(f"✗ {site}: {pin} does not resolve on GitHub ({detail}); pin a commit that exists")
    infra = sorted(p for p, (state, _) in results.items() if state == "infra")

    print(
        f"action pins: {len(pins)} distinct, {len(pins) - len(todo)} cached, "
        f"{len(todo)} asked, {len(unpinned)} unpinned"
    )
    if failed:
        # A definite failure wins. The pins that could not be checked are
        # named without the HARNESS ERROR prefix, which would make the gate
        # report this red as infrastructure.
        for pin in infra:
            print(f"note: could not check {pin} either: {results[pin][1]}")
        return 1
    if infra:
        for pin in infra:
            print(f"HARNESS ERROR: could not check {pin}: {results[pin][1]} (used at {', '.join(sites[pin])})")
        return 2
    print("✓ every workflow action pin resolves")
    return 0


if __name__ == "__main__":
    sys.exit(main())
