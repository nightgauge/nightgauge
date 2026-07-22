#!/usr/bin/env python3
"""
retro-engine.py — Deterministic parsing engine for the nightgauge-retro skill.

Main entry point that orchestrates:
  1. Session log parsing
  2. Execution history (JSONL) parsing
  3. Batch state parsing
  4. Failure classification (rule-based, first-match-wins)
  5. Cross-referencing with budget/model routing data
  6. JSON + Markdown report generation

Invoked by the SKILL.md workflow via Bash:
    python3 scripts/retro/retro-engine.py --period 7 --format both

Source: skills/nightgauge-retro/SKILL.md
"""

import argparse
import json
import os
import sys
from datetime import datetime, timedelta, timezone

# Local imports — package lives alongside this script
_SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
if _SCRIPT_DIR not in sys.path:
    sys.path.insert(0, _SCRIPT_DIR)

from parsers.session_log_parser import SessionLogParser
from parsers.history_parser import HistoryParser
from parsers.batch_state_parser import BatchStateParser
from classifiers.failure_classifier import FailureClassifier
from cross_reference import CrossReferenceEngine
from report_generator import ReportGenerator


def parse_args(argv=None):
    """Parse CLI arguments."""
    parser = argparse.ArgumentParser(
        description="Retro engine — analyze pipeline failures and generate reports.",
    )
    parser.add_argument(
        "--issue",
        type=int,
        default=None,
        help="Analyze failures for a specific issue number",
    )
    parser.add_argument(
        "--since",
        type=str,
        default=None,
        help="Analyze failures since date (YYYY-MM-DD)",
    )
    parser.add_argument(
        "--period",
        type=int,
        default=7,
        help="Analyze last N days (default: 7)",
    )
    parser.add_argument(
        "--all-failures",
        action="store_true",
        default=False,
        help="Include all failures regardless of date filter",
    )
    parser.add_argument(
        "--format",
        dest="output_format",
        choices=["summary", "json", "both"],
        default="both",
        help="Output format (default: both)",
    )
    parser.add_argument(
        "--workspace",
        type=str,
        default=".",
        help="Workspace root path (default: current directory)",
    )
    return parser.parse_args(argv)


def compute_since_date(args):
    """Compute the effective since_date from args."""
    if args.since:
        return args.since
    cutoff = datetime.now(timezone.utc) - timedelta(days=args.period)
    return cutoff.strftime("%Y-%m-%d")


def discover_data_sources(workspace, since_date, issue_filter, all_failures):
    """Discover which data sources are available and parse them.

    Returns (batch_data, history_data, log_data, data_sources).
    """
    logs_dir = os.path.join(workspace, ".nightgauge", "logs")
    history_dir = os.path.join(workspace, ".nightgauge", "pipeline", "history")
    batch_state_path = os.path.join(
        workspace, ".nightgauge", "pipeline", "batch-state.json"
    )

    data_sources = []
    batch_data = None
    history_data = None
    log_data = None

    # Batch state
    if os.path.isfile(batch_state_path):
        batch_parser = BatchStateParser()
        batch_data = batch_parser.parse_file(batch_state_path)
        if batch_data:
            data_sources.append("batch-state")

    # Execution history
    if os.path.isdir(history_dir):
        history_parser = HistoryParser()
        history_data = history_parser.parse_directory(
            history_dir,
            since_date=since_date,
            issue_filter=issue_filter,
            all_failures=all_failures,
        )
        if history_data and (history_data.get("runs") or history_data.get("failures")):
            data_sources.append("history")

    # Session logs
    if os.path.isdir(logs_dir):
        log_parser = SessionLogParser()
        log_data = log_parser.parse_directory(
            logs_dir,
            since_date=since_date,
            issue_filter=issue_filter,
        )
        if log_data:
            data_sources.append("logs")

    return batch_data, history_data, log_data, data_sources


def build_unified_failures(batch_data, history_data, log_data, issue_filter):
    """Merge failure events from all sources into a unified list.

    Deduplicates by issue_number — prefers history > batch-state > logs.
    """
    events_by_issue = {}

    # History failures (highest priority)
    if history_data:
        for run in history_data.get("failures", []):
            issue_num = run.get("issue_number")
            if issue_num is None:
                continue
            if issue_filter and issue_num != issue_filter:
                continue

            # Extract failed stages from history run format
            failed_stages = []
            stages = run.get("stages", {})
            for stage_name, stage_data in stages.items():
                if isinstance(stage_data, dict) and stage_data.get("status") in (
                    "failed",
                    "error",
                    "timeout",
                    "cancelled",
                ):
                    failed_stages.append(stage_name)

            events_by_issue[issue_num] = {
                "issue_number": issue_num,
                "title": run.get("title", ""),
                "outcome": run.get("outcome", "failed"),
                "failed_stages": failed_stages,
                "log_signals": [],
                "errors": [],
                "estimated_cost_usd": run.get("tokens", {}).get(
                    "estimated_cost_usd", 0
                ),
                "total_duration_ms": run.get("total_duration_ms", 0),
                "token_usage": run.get("tokens", {}),
                "stages": stages,
                "size": run.get("size"),
                "labels": run.get("labels", []),
                "sources": ["history"],
            }

    # Batch state failures
    if batch_data:
        for failure in batch_data.get("failures", []):
            issue_num = failure.get("issue_number")
            if issue_num is None:
                continue
            if issue_filter and issue_num != issue_filter:
                continue

            if issue_num in events_by_issue:
                # Merge batch info into existing event
                events_by_issue[issue_num]["sources"].append("batch-state")
                if not events_by_issue[issue_num]["failed_stages"]:
                    events_by_issue[issue_num]["failed_stages"] = failure.get(
                        "failed_stages", []
                    )
            else:
                events_by_issue[issue_num] = {
                    "issue_number": issue_num,
                    "title": failure.get("title", ""),
                    "outcome": failure.get("status", "failed"),
                    "failed_stages": failure.get("failed_stages", []),
                    "log_signals": [],
                    "errors": [],
                    "estimated_cost_usd": failure.get("token_usage", {}).get(
                        "cost_usd", 0
                    ),
                    "total_duration_ms": failure.get("duration_ms", 0),
                    "token_usage": failure.get("token_usage", {}),
                    "stages": {},
                    "size": None,
                    "labels": [],
                    "sources": ["batch-state"],
                }

    # Session log signals — enrich existing events or create new ones
    if log_data:
        for log_result in log_data:
            issue_num = log_result.get("issue_number")
            if issue_num is None:
                continue
            if issue_filter and issue_num != issue_filter:
                continue

            # Collect log signals (error messages)
            signals = []
            for error in log_result.get("errors", []):
                signals.append(error.get("message", ""))
            for exceeded in log_result.get("budget_exceeded", []):
                signals.append(exceeded.get("message", ""))

            if issue_num in events_by_issue:
                events_by_issue[issue_num]["log_signals"].extend(signals)
                events_by_issue[issue_num]["errors"].extend(
                    log_result.get("errors", [])
                )
                if "logs" not in events_by_issue[issue_num]["sources"]:
                    events_by_issue[issue_num]["sources"].append("logs")
            elif log_result.get("final_status") == "failure":
                # Create new event from logs only
                failed_stages = []
                for error in log_result.get("errors", []):
                    stage = error.get("stage")
                    if stage and stage not in failed_stages:
                        failed_stages.append(stage)

                events_by_issue[issue_num] = {
                    "issue_number": issue_num,
                    "title": "",
                    "outcome": "failed",
                    "failed_stages": failed_stages,
                    "log_signals": signals,
                    "errors": log_result.get("errors", []),
                    "estimated_cost_usd": 0,
                    "total_duration_ms": 0,
                    "token_usage": {},
                    "stages": {},
                    "size": None,
                    "labels": [],
                    "sources": ["logs"],
                }

    return list(events_by_issue.values())


def determine_scope(args, batch_data, history_data, data_sources, unified_failures):
    """Build the scope dict for the report."""
    since_date = compute_since_date(args)
    today = datetime.now(timezone.utc).strftime("%Y-%m-%d")

    issues_analyzed = 0
    scope_type = "date-range"

    if args.issue:
        scope_type = "single-issue"
        issues_analyzed = 1
    elif batch_data and not args.all_failures and not args.since:
        scope_type = "batch"
        issues_analyzed = batch_data.get("total_issues", 0)
    else:
        scope_type = "date-range"
        # Count unique issues from history
        if history_data:
            seen = set()
            for run in history_data.get("runs", []):
                num = run.get("issue_number")
                if num is not None:
                    seen.add(num)
            issues_analyzed = len(seen)

        # Supplement with batch count if no history
        if issues_analyzed == 0 and batch_data:
            issues_analyzed = batch_data.get("total_issues", 0)

    return {
        "type": scope_type,
        "from": since_date,
        "to": today,
        "issue_filter": args.issue,
        "issues_analyzed": max(issues_analyzed, len(unified_failures)),
        "failure_count": len(unified_failures),
        "data_sources": data_sources,
    }


def main(argv=None):
    """Main entry point."""
    args = parse_args(argv)
    workspace = os.path.abspath(args.workspace)
    since_date = compute_since_date(args)

    # Check Python3 availability (we're already running, but verify workspace)
    nightgauge_dir = os.path.join(workspace, ".nightgauge")
    if not os.path.isdir(nightgauge_dir):
        print(
            f"WARNING: .nightgauge/ not found in {workspace}. "
            "Some data sources may be unavailable.",
            file=sys.stderr,
        )

    # Discover and parse data sources
    batch_data, history_data, log_data, data_sources = discover_data_sources(
        workspace,
        since_date,
        args.issue,
        args.all_failures,
    )

    if not data_sources:
        print("No pipeline data sources found.")
        print(f"  Workspace: {workspace}")
        print(f"  Expected locations:")
        print(f"    .nightgauge/logs/*_session.log")
        print(f"    .nightgauge/pipeline/history/*.jsonl")
        print(f"    .nightgauge/pipeline/batch-state.json")
        return 0

    # Build unified failure events
    unified_failures = build_unified_failures(
        batch_data,
        history_data,
        log_data,
        args.issue,
    )

    # Build scope
    scope = determine_scope(
        args, batch_data, history_data, data_sources, unified_failures
    )

    # No failures found
    if not unified_failures:
        print("No pipeline failures found in the specified scope.")
        print(f"  Scope: {scope['type']} ({scope['from']} to {scope['to']})")
        print(
            f"  Issues analyzed: {scope['issues_analyzed']} (all completed successfully)"
        )
        print(f"  Data sources: {', '.join(data_sources)}")
        return 0

    # Classify failures
    classifier = FailureClassifier()
    classified = classifier.classify_all(unified_failures)

    # Cross-reference with budget/routing data
    xref_engine = CrossReferenceEngine(workspace_root=workspace)
    enriched = xref_engine.cross_reference_all(classified)

    # Generate report
    generator = ReportGenerator()
    report = generator.generate_json_report(scope, enriched)

    # Output
    output_dir = os.path.join(workspace, ".nightgauge", "pipeline")

    if args.output_format in ("json", "both"):
        filepath = generator.write_json_report(report, output_dir)
        if args.output_format == "json":
            # JSON-only: write to stdout
            print(json.dumps(report, indent=2))
        print(f"\nJSON report written: {filepath}", file=sys.stderr)

    if args.output_format in ("summary", "both"):
        markdown = generator.generate_markdown_report(report)
        print(markdown)

    return 0


if __name__ == "__main__":
    sys.exit(main())
