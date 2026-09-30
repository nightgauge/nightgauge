"""
batch_state_parser.py — batch-state.json parser for the retro engine.

Reads the batch state file in the clone's pipeline directory:
  <git-common-dir>/nightgauge/pipeline/batch-state.json (ADR-024 § 7)

The file shape matches what ``nightgauge pipeline batch-failures`` reads:
``status``, ``started_at``, ``updated_at`` and an ``issueResults`` list whose
entries carry ``issueNumber``, ``title``, ``status``, ``completedStages``,
``durationMs`` and ``tokenUsage``.
"""

import json
import warnings
from typing import Optional

# Mirrors internal/cmd/batchfailures canonicalStages; a stage missing from completedStages counts as failed.
CANONICAL_STAGES = (
    "pipeline-start",
    "issue-pickup",
    "feature-planning",
    "feature-dev",
    "feature-validate",
    "pr-create",
    "pr-merge",
    "pipeline-finish",
)


class BatchStateParser:
    """Parse batch-state.json. Stateless."""

    def parse_file(self, filepath: str) -> Optional[dict]:
        """Parse *filepath*.

        Returns None when the file is missing, unreadable or not a JSON object.
        Otherwise a dict with keys: batch_status, started_at, updated_at,
        total_issues, failures (list of dicts with issue_number, title, status,
        completed_stages, failed_stages, duration_ms, token_usage).
        """
        try:
            with open(filepath, encoding="utf-8") as fh:
                raw = json.load(fh)
        except (OSError, json.JSONDecodeError) as exc:
            warnings.warn(f"{filepath}: cannot read batch state: {exc}", stacklevel=2)
            return None
        if not isinstance(raw, dict):
            warnings.warn(f"{filepath}: expected JSON object", stacklevel=2)
            return None

        results = raw.get("issueResults")
        if not isinstance(results, list):
            results = []

        failures = []
        for item in results:
            if not isinstance(item, dict) or item.get("issueNumber") is None:
                continue
            completed = item.get("completedStages")
            if not isinstance(completed, list):
                completed = []
            failed = [s for s in CANONICAL_STAGES if s not in completed]
            status = item.get("status") or "failed"
            if status == "completed" and not failed:
                continue
            usage = item.get("tokenUsage")
            failures.append(
                {
                    "issue_number": item["issueNumber"],
                    "title": item.get("title") or "",
                    "status": status,
                    "completed_stages": completed,
                    "failed_stages": failed,
                    "duration_ms": item.get("durationMs") or 0,
                    "token_usage": usage if isinstance(usage, dict) else {},
                }
            )

        return {
            "batch_status": raw.get("status", ""),
            "started_at": raw.get("started_at", ""),
            "updated_at": raw.get("updated_at", ""),
            "total_issues": len(results),
            "failures": failures,
        }
