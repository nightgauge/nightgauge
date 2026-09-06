#!/usr/bin/env bash
# Report the pipeline runs still in flight in this workspace's VS Code window.
#
# Installing the extension ends with a window reload, and `deactivate` calls
# `abortAll()` — so a reload KILLS every running slot, each of which then
# restarts from scratch and re-spends its planning. `nightgauge autonomous
# stop` does not do that: Stop stops admitting new work and lets the running
# slots finish. The gap this closes is that nothing told the operator WHEN the
# last one had finished, so "stopped" and "safe to reload" looked identical
# (#1511).
#
# Usage:
#   check-running-pipelines.sh [repo-root]
#
# Env:
#   NIGHTGAUGE_BIN  path to the nightgauge binary (default: <repo-root>/bin/nightgauge)
#
# Exit codes:
#   0  no pipeline is running, OR the answer is unknown (no binary, no daemon,
#      unparseable output). UNKNOWN IS NOT AN ALARM: this script guards a
#      convenience, and a missing daemon is the normal state of a fresh clone
#      and of every CI runner.
#   1  at least one pipeline is running; the rows are printed on stdout.
#
# It only reports. The decision — prompt, skip, continue — belongs to the
# caller, because killing the in-flight runs is sometimes the right choice
# (notably when the fix being installed is what those runs keep failing on).

set -uo pipefail

ROOT="${1:-$(cd "$(dirname "$0")/../../.." && pwd)}"
BIN="${NIGHTGAUGE_BIN:-$ROOT/bin/nightgauge}"

[[ -x "$BIN" ]] || exit 0
command -v node &>/dev/null || exit 0

STATUS_JSON=$("$BIN" autonomous status --json 2>/dev/null) || exit 0
[[ -n "$STATUS_JSON" ]] || exit 0

# node, not jq: jq is not a declared dependency of this repo and node is
# (the extension is built with it three lines later in dev-install.sh).
SUMMARY=$(printf '%s' "$STATUS_JSON" | node -e '
  let raw = "";
  process.stdin.on("data", (d) => (raw += d));
  process.stdin.on("end", () => {
    let s;
    try {
      s = JSON.parse(raw);
    } catch {
      process.exit(0); // unparseable — unknown, not an alarm
    }
    // running_pipelines_known false means no daemon answered. An empty list
    // then means "we did not ask", not "nothing is running", and must never
    // be rendered as safe.
    if (!s || s.running_pipelines_known !== true) process.exit(0);
    const runs = Array.isArray(s.running_pipelines) ? s.running_pipelines : [];
    if (runs.length === 0) process.exit(0);
    for (const r of runs) {
      const repo = String(r.repo || "");
      const short = repo.includes("/") ? repo.slice(repo.lastIndexOf("/") + 1) : repo;
      const bits = [`#${r.issueNumber}`, short].filter(Boolean);
      if (r.stage) bits.push(`(${r.stage})`);
      bits.push(r.source === "autonomous" ? "[autonomous]" : "[manual]");
      if (r.stale) bits.push("— no progress recently, may already be finished");
      process.stdout.write("    " + bits.join(" ") + "\n");
    }
    process.exit(1);
  });
')
RC=$?

if [[ $RC -eq 1 ]]; then
  printf '%s\n' "$SUMMARY"
  exit 1
fi
exit 0
