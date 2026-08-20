#!/usr/bin/env python3
"""Write the discovery run records the VSCode Discovery tab reads.

The Discovery tab, `DiscoveryActivityService`, its unit tests and
`docs/SCHEDULED_DISCOVERY.md` all shipped reading these files. Nothing ever
wrote them (#753) — the tab was correct code pointed at a path no producer
existed for, so it rendered "No discovery activity yet" forever and looked
like a bug in the reader.

This script is that producer's schema. It is deliberately the ONLY place the
JSON shape is spelled out, so the field names cannot drift away from
`packages/nightgauge-vscode/src/services/DiscoveryActivityService.ts` without
a test noticing — `tests/arrival/dashboardDiscoveryTab.test.ts` runs this
script and asserts the values reach the rendered HTML.

Two verbs, used to bracket a scheduled run:

  open   Write a `status: running` record before any work starts. A run that
         dies mid-flight therefore still leaves evidence in the tab; a
         producer that only wrote on success would be indistinguishable from
         a producer that never ran, which is the failure this issue is about.

  close  Re-read whatever is on disk (the skill rewrites the record in place
         when it creates issues), stamp the terminal status and
         `completed_at`, and backfill any key the reader requires. Whatever
         the skill wrote between open and close survives.

Usage:

  discovery-run-record.py open  --kind release-watch --workspace . \\
      --provider claude-code --source anthropics/claude-code \\
      --triggered-by schedule --since-version 2.1.74 --new-version 2.1.80

  discovery-run-record.py close --kind release-watch --workspace . \\
      --provider claude-code --status completed

  discovery-run-record.py open  --kind continuous-improvement --workspace . \\
      --triggered-by schedule --mode dogfood --create-issues --dry-run

  discovery-run-record.py close --kind continuous-improvement --workspace . \\
      --status failed --error "claude CLI exited 1"

Exit codes: 0 success, 2 bad arguments or unwritable target.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import tempfile
from datetime import datetime, timezone
from pathlib import Path

# Bumped only when the reader has to learn a new shape. The reader tolerates
# its absence, but writing it means a future migration can tell records apart.
SCHEMA_VERSION = "1.0"

RELEASE_WATCH = "release-watch"
CONTINUOUS_IMPROVEMENT = "continuous-improvement"


def utc_now() -> str:
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def record_path(workspace: Path, kind: str, provider: str) -> Path:
    """The exact path DiscoveryActivityService globs for.

    Release-watch is per-provider: the service matches
    `^creation-log.*\\.json$` and aggregates every match, so one file per
    provider is the supported layout. Continuous-improvement keeps a single
    `latest.json`.
    """
    if kind == RELEASE_WATCH:
        return (
            workspace
            / ".nightgauge"
            / "release-watch"
            / f"creation-log-{provider}.json"
        )
    return workspace / ".nightgauge" / "improvement-runs" / "latest.json"


def read_existing(path: Path) -> dict:
    try:
        with path.open(encoding="utf-8") as handle:
            loaded = json.load(handle)
    except (OSError, ValueError):
        # A corrupt or absent record is not a failure to close a run over:
        # the reader treats an unparseable file as "no run", and refusing to
        # write here would make one bad record permanent.
        return {}
    return loaded if isinstance(loaded, dict) else {}


def write_atomic(path: Path, payload: dict) -> None:
    """Write via a temp file in the same directory, then rename.

    The runner pushes this file to the state branch in a later step and the
    extension polls it locally; a reader that catches a half-written file
    parses nothing and reports "no discovery runs", which reads exactly like
    the bug this script exists to fix.
    """
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp_name = tempfile.mkstemp(
        dir=str(path.parent), prefix=".discovery-", suffix=".tmp"
    )
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            json.dump(payload, handle, indent=2, sort_keys=False)
            handle.write("\n")
        os.replace(tmp_name, path)
    except BaseException:
        try:
            os.unlink(tmp_name)
        except OSError:
            pass
        raise


def open_release_watch(args: argparse.Namespace) -> dict:
    return {
        "schema_version": SCHEMA_VERSION,
        # provider/source are not read by the dashboard but ARE read by
        # `nightgauge release notify-findings` (internal/cmd/release/notify.go),
        # which parses this same file for the alert sink.
        "provider": args.provider,
        "source": args.source,
        "run_started_at": args.run_started_at or utc_now(),
        "triggered_by": args.triggered_by,
        "new_version": args.new_version,
        "since_version": args.since_version,
        "status": "running",
        "issues_created": [],
        "issues_backlogged": [],
        "issues_deduped": [],
        "completed_at": None,
        "error": None,
    }


def open_continuous_improvement(args: argparse.Namespace) -> dict:
    return {
        "schema_version": SCHEMA_VERSION,
        "run_started_at": args.run_started_at or utc_now(),
        "triggered_by": args.triggered_by,
        "mode": args.mode,
        "create_issues": args.create_issues,
        "dry_run": args.dry_run,
        "status": "running",
        "proposals_created": [],
        "proposals_backlogged": [],
        "completed_at": None,
        "error": None,
    }


def close_record(existing: dict, kind: str, args: argparse.Namespace) -> dict:
    payload = dict(existing)
    payload.setdefault("schema_version", SCHEMA_VERSION)
    payload.setdefault("run_started_at", utc_now())
    payload.setdefault("triggered_by", args.triggered_by)
    if kind == RELEASE_WATCH:
        payload.setdefault("provider", args.provider)
        payload.setdefault("source", args.source)
        payload.setdefault("new_version", "")
        payload.setdefault("since_version", "")
        for key in ("issues_created", "issues_backlogged", "issues_deduped"):
            if not isinstance(payload.get(key), list):
                payload[key] = []
    else:
        payload.setdefault("mode", args.mode)
        payload.setdefault("create_issues", args.create_issues)
        payload.setdefault("dry_run", args.dry_run)
        for key in ("proposals_created", "proposals_backlogged"):
            if not isinstance(payload.get(key), list):
                payload[key] = []
    payload["status"] = args.status
    payload["completed_at"] = args.completed_at or utc_now()
    payload["error"] = args.error or None
    return payload


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    parser.add_argument("verb", choices=["open", "close"])
    parser.add_argument(
        "--kind", required=True, choices=[RELEASE_WATCH, CONTINUOUS_IMPROVEMENT]
    )
    parser.add_argument(
        "--workspace", default=".", help="Repository root holding .nightgauge/"
    )
    parser.add_argument(
        "--provider", default="claude-code", help="release-watch provider slug"
    )
    parser.add_argument(
        "--source", default="anthropics/claude-code", help="release-watch source repo"
    )
    parser.add_argument("--triggered-by", default="schedule")
    parser.add_argument("--run-started-at", default="")
    parser.add_argument("--completed-at", default="")
    parser.add_argument("--new-version", default="")
    parser.add_argument("--since-version", default="")
    parser.add_argument("--mode", default="dogfood", choices=["dogfood", "customer"])
    parser.add_argument("--create-issues", action="store_true")
    parser.add_argument("--dry-run", action="store_true")
    parser.add_argument(
        "--status", default="completed", choices=["running", "completed", "failed"]
    )
    parser.add_argument("--error", default="")
    return parser


def main(argv: list[str]) -> int:
    args = build_parser().parse_args(argv)
    workspace = Path(args.workspace).resolve()
    path = record_path(workspace, args.kind, args.provider)

    if args.verb == "open":
        payload = (
            open_release_watch(args)
            if args.kind == RELEASE_WATCH
            else open_continuous_improvement(args)
        )
    else:
        payload = close_record(read_existing(path), args.kind, args)

    try:
        write_atomic(path, payload)
    except OSError as err:
        print(f"discovery-run-record: cannot write {path}: {err}", file=sys.stderr)
        return 2

    print(f"discovery-run-record: {args.verb} {args.kind} -> {path}")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
