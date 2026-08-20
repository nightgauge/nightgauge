#!/usr/bin/env python3
"""Resolve the `autonomous_discovery` switches for a scheduled discovery run.

`.nightgauge/config.yaml` owns whether the loop may run and whether it may
create issues. The two scheduled workflows read it through this script rather
than through inline YAML-in-YAML, so the precedence is written down once and
both workflows cannot drift apart.

Resolution, in order:

  enabled       autonomous_discovery.enabled AND the per-task
                scheduled_tasks.<task>.enabled. Either being false skips the
                whole run — a task switch that only dimmed part of the run
                would leave a half-executed loop nobody asked for.

  create_issues enabled AND NOT autonomous_discovery.kill_switch. The kill
                switch is documented as "pause issue creation, detection
                continues", so it must not stop the run: the tab still gets a
                record and last-seen still advances.

  score_threshold  autonomous_discovery.score_threshold, default 70 — the same
                default as release.DefaultAlertMinScore in
                internal/cmd/release/notify.go.

FAIL CLOSED. A missing, unreadable or unparseable config yields
`enabled=false` with a reason, never a default-on run. An autonomous loop that
files issues because it could not find its own off switch is strictly worse
than one that does nothing.

Outputs `key=value` lines on stdout, and appends the same lines to the file
named by $GITHUB_OUTPUT when that variable is set.

Exit code is always 0: "the loop is switched off" is an answer, not a failure.
"""

from __future__ import annotations

import argparse
import os
import sys
from pathlib import Path

try:
    import yaml
except ImportError:  # pragma: no cover - the workflow installs PyYAML explicitly
    print(
        "discovery-config-gate: PyYAML is not installed; "
        "run `python3 -m pip install PyYAML` first",
        file=sys.stderr,
    )
    sys.exit(2)

DEFAULT_SCORE_THRESHOLD = 70


def as_bool(value: object, default: bool) -> bool:
    """YAML already gives us real booleans; strings arrive from overrides."""
    if isinstance(value, bool):
        return value
    if isinstance(value, str):
        return value.strip().lower() in {"true", "yes", "1", "on"}
    return default


def resolve(config_path: Path, task: str) -> dict[str, object]:
    if not config_path.is_file():
        return {
            "enabled": False,
            "create_issues": False,
            "kill_switch": True,
            "score_threshold": DEFAULT_SCORE_THRESHOLD,
            "reason": f"no config at {config_path}",
        }

    try:
        with config_path.open(encoding="utf-8") as handle:
            config = yaml.safe_load(handle) or {}
    except (OSError, yaml.YAMLError) as err:
        return {
            "enabled": False,
            "create_issues": False,
            "kill_switch": True,
            "score_threshold": DEFAULT_SCORE_THRESHOLD,
            "reason": f"unreadable config: {err}",
        }

    if not isinstance(config, dict):
        return {
            "enabled": False,
            "create_issues": False,
            "kill_switch": True,
            "score_threshold": DEFAULT_SCORE_THRESHOLD,
            "reason": "config root is not a mapping",
        }

    discovery = config.get("autonomous_discovery") or {}
    tasks = config.get("scheduled_tasks") or {}
    task_block = tasks.get(task) or {}
    if not isinstance(discovery, dict):
        discovery = {}
    if not isinstance(task_block, dict):
        task_block = {}

    # Both default to FALSE when absent. The repository's own config sets them
    # explicitly; a workspace that has never heard of autonomous discovery must
    # not acquire it by upgrading Nightgauge.
    discovery_enabled = as_bool(discovery.get("enabled"), False)
    task_enabled = as_bool(task_block.get("enabled"), False)
    kill_switch = as_bool(discovery.get("kill_switch"), True)

    try:
        score_threshold = int(discovery.get("score_threshold", DEFAULT_SCORE_THRESHOLD))
    except (TypeError, ValueError):
        score_threshold = DEFAULT_SCORE_THRESHOLD

    enabled = discovery_enabled and task_enabled
    if not discovery_enabled:
        reason = "autonomous_discovery.enabled is false"
    elif not task_enabled:
        reason = f"scheduled_tasks.{task}.enabled is false"
    elif kill_switch:
        reason = "autonomous_discovery.kill_switch is true — detection only"
    else:
        reason = "enabled"

    return {
        "enabled": enabled,
        "create_issues": enabled and not kill_switch,
        "kill_switch": kill_switch,
        "score_threshold": score_threshold,
        "reason": reason,
    }


def emit(values: dict[str, object]) -> None:
    lines = []
    for key, value in values.items():
        rendered = str(value).lower() if isinstance(value, bool) else str(value)
        lines.append(f"{key}={rendered}")
    text = "\n".join(lines)
    print(text)
    github_output = os.environ.get("GITHUB_OUTPUT")
    if github_output:
        with open(github_output, "a", encoding="utf-8") as handle:
            handle.write(text + "\n")


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    parser.add_argument(
        "--task",
        required=True,
        choices=["release_watch", "continuous_improvement"],
        help="scheduled_tasks key whose per-task switch also has to be on",
    )
    parser.add_argument(
        "--workspace", default=".", help="Repository root holding .nightgauge/"
    )
    args = parser.parse_args(argv)

    config_path = Path(args.workspace).resolve() / ".nightgauge" / "config.yaml"
    emit(resolve(config_path, args.task))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
