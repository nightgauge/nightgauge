#!/usr/bin/env bash
# Regression tests for the workflow action SHA-pin gate
# (`scripts/check-action-pins.py`, #1782).
#
# Hermetic: the gate is pointed (GITHUB_API_URL) at a stub GitHub API served
# from a thread inside the harness below, so no test reaches the network and
# none can leave a server process behind. Each case writes a throwaway
# workflow tree and asserts the exit code and the lines the gate prints —
# including the three states it must keep apart: a pin that resolves (0), a pin
# that does not (1), and a pin nobody could ask about (2, a HARNESS ERROR line
# that scripts/ci-local.sh reports as INFRASTRUCTURE, never as a pass).
#
# Run: bash scripts/test-check-action-pins.sh
# Also run by .github/workflows/lint.yml and scripts/ci-local.sh.

set -uo pipefail
cd "$(git rev-parse --show-toplevel)"

exec python3 - "$PWD" <<'PY'
import atexit
import http.server
import importlib.util
import json
import os
import shutil
import subprocess
import sys
import tempfile
import threading
from pathlib import Path

REPO = Path(sys.argv[1])
GATE = REPO / "scripts" / "check-action-pins.py"
CLASSIFY = REPO / "scripts" / "lib" / "ci_local_failures.sh"

OK = "1" * 40
OK2 = "2" * 40
MISSING_422 = "3" * 40
MISSING_404 = "4" * 40
RATE = "5" * 40
FLAKY = "6" * 40  # 500, then 200: the one retry must absorb it

# sha -> responses, consumed in order; the last one repeats.
script = {
    OK: [200], OK2: [200], MISSING_422: [422], MISSING_404: [404],
    RATE: [403], FLAKY: [500, 200],
}
calls: list[tuple[str, str]] = []
lock = threading.Lock()


class Stub(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        sha = self.path.rstrip("/").rsplit("/", 1)[-1]
        with lock:
            calls.append((self.path, self.headers.get("Authorization", "")))
            seq = script.get(sha, [404])
            code = seq.pop(0) if len(seq) > 1 else seq[0]
        self.send_response(code)
        if code == 403:
            self.send_header("x-ratelimit-remaining", "0")
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(b"{}")

    def log_message(self, *args):
        pass


server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Stub)
threading.Thread(target=server.serve_forever, daemon=True).start()
API = f"http://127.0.0.1:{server.server_address[1]}"
DEAD = "http://127.0.0.1:9"  # discard port: connection refused

passed = failed = 0
made: list[Path] = []
atexit.register(lambda: [shutil.rmtree(d, ignore_errors=True) for d in made])


def scratch() -> Path:
    d = Path(tempfile.mkdtemp())
    made.append(d)
    return d


def workflow_tree(lines: list[str]) -> Path:
    root = scratch()
    wf = root / ".github" / "workflows"
    wf.mkdir(parents=True)
    body = "jobs:\n  a:\n    runs-on: ubuntu-latest\n    steps:\n" + "".join(f"      {l}\n" for l in lines)
    (wf / "ci.yml").write_text(body)
    return root


def run(root: Path, api: str, cache: Path) -> tuple[int, str]:
    env = {k: v for k, v in os.environ.items() if k not in ("GH_TOKEN", "GITHUB_TOKEN")}
    env.update(GITHUB_API_URL=api, GITHUB_TOKEN="stub-token", NIGHTGAUGE_ACTION_PIN_CACHE=str(cache))
    p = subprocess.run([sys.executable, str(GATE), str(root)], capture_output=True, text=True, env=env, timeout=120)
    return p.returncode, p.stdout + p.stderr


def check(name: str, cond: bool, out: str = "") -> None:
    global passed, failed
    if cond:
        passed += 1
        print(f"  ✓ {name}")
    else:
        failed += 1
        print(f"  ✗ {name}\n{out}")


def classify(out: str) -> str:
    with tempfile.NamedTemporaryFile("w", suffix=".log", delete=False) as f:
        f.write(out)
    try:
        return subprocess.run(
            ["bash", "-c", f'source "{CLASSIFY}"; classify_failure "$1" 2', "_", f.name],
            capture_output=True, text=True, timeout=30,
        ).stdout.strip()
    finally:
        os.unlink(f.name)


tmp = scratch()

# 1. Every pin resolves: exit 0, each distinct pin asked once, with the token,
#    and the resolved pins cached. Local actions, docker images and commented
#    lines are not GitHub refs.
root = workflow_tree([
    f"- uses: actions/checkout@{OK} # v7",
    f"- uses: actions/checkout@{OK}",
    f"- uses: github/codeql-action/init@{OK2}",
    f"- uses: github/codeql-action/analyze@{OK2}",
    "- uses: ./.github/actions/local",
    "- uses: docker://alpine:3.20",
    "# - uses: actions/setup-node@v4",
])
cache = tmp / "warm.json"
code, out = run(root, API, cache)
check("all pins resolve: exit 0", code == 0, out)
check("each distinct owner/repo@sha is asked once", len(calls) == 2, f"{calls}\n{out}")
check("the request carries the token", all(a == "Bearer stub-token" for _, a in calls), str(calls))
check("a subpath action resolves against its repository",
      any(p == f"/repos/github/codeql-action/commits/{OK2}" for p, _ in calls), str(calls))
resolved = set(json.loads(cache.read_text())["resolved"])
check("resolved pins are cached", resolved == {f"actions/checkout@{OK}", f"github/codeql-action@{OK2}"}, str(resolved))

# 2. A warm cache needs no network: the same tree passes against a dead API.
calls.clear()
code, out = run(root, DEAD, cache)
check("warm cache passes offline", code == 0 and "2 cached, 0 asked" in out, out)

# 3. A SHA that does not exist (422, what GitHub answers) fails, naming the
#    file and line, and is not cached.
root = workflow_tree([f"- uses: actions/checkout@{OK}", f"- uses: actions/setup-node@{MISSING_422} # bad"])
cache = tmp / "missing.json"
code, out = run(root, API, cache)
check("a nonexistent SHA fails: exit 1", code == 1, out)
check("the failure names file and line", f".github/workflows/ci.yml:6: actions/setup-node@{MISSING_422} does not resolve" in out, out)
check("a failure is classified as an assertion, not infrastructure", classify(out) == "assert", out)
check("a failure is never cached", f"actions/setup-node@{MISSING_422}" not in cache.read_text(), cache.read_text())

# 4. A 404 (a repository that is gone) fails the same way.
code, out = run(workflow_tree([f"- uses: gone/action@{MISSING_404}"]), API, tmp / "404.json")
check("a 404 fails: exit 1", code == 1 and f"gone/action@{MISSING_404} does not resolve" in out, out)

# 5. A tag or branch is not a pin.
code, out = run(workflow_tree(["- uses: actions/setup-node@v4"]), API, tmp / "tag.json")
check("a tag ref fails: exit 1", code == 1 and "ci.yml:5: uses actions/setup-node@v4, which is not pinned" in out, out)

# 6. No network and a cold cache: neither a pass nor a broken pin.
root = workflow_tree([f"- uses: actions/checkout@{OK}"])
code, out = run(root, DEAD, tmp / "cold.json")
check("offline with a cold cache: exit 2", code == 2, out)
check("offline prints a HARNESS ERROR line", "HARNESS ERROR: could not check actions/checkout@" in out, out)
check("ci-local classifies it as INFRASTRUCTURE", classify(out) == "infra", out)

# 7. Rate limited: infrastructure too.
code, out = run(workflow_tree([f"- uses: actions/checkout@{RATE}"]), API, tmp / "rate.json")
check("rate limited: exit 2 with HARNESS ERROR", code == 2 and "rate limited" in out and classify(out) == "infra", out)

# 8. One 5xx is retried once.
code, out = run(workflow_tree([f"- uses: actions/checkout@{FLAKY}"]), API, tmp / "retry.json")
check("a single 5xx is retried and passes", code == 0, out)

# 9. A definite failure beside an unanswerable pin is a failure, and is not
#    reported as infrastructure.
script[RATE] = [403]
code, out = run(workflow_tree([f"- uses: a/b@{MISSING_422}", f"- uses: c/d@{RATE}"]), API, tmp / "mixed.json")
check("missing + rate limited: exit 1, classified as an assertion",
      code == 1 and "HARNESS ERROR" not in out and classify(out) == "assert", out)

# 10. The committed workflows are well-formed for the gate: every uses: ref
#     in this repository is a commit SHA (checked offline: no requests made).
spec = importlib.util.spec_from_file_location("gate", GATE)
gate = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gate)
sites = gate.scan(REPO)
unpinned = [k for k in sites if not gate.SHA.match(k.partition("@")[2])]
check("this repository's workflows pin every action to a SHA", sites and not unpinned, str(unpinned))

server.shutdown()
print(f"\ncheck-action-pins: {passed} passed, {failed} failed")
sys.exit(1 if failed else 0)
PY
