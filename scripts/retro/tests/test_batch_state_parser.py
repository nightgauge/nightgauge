"""
test_batch_state_parser.py — Unit tests for BatchStateParser and engine import.

Run with:
    pytest scripts/retro/tests/test_batch_state_parser.py
"""

import json
import os
import subprocess
import sys

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from parsers.batch_state_parser import BatchStateParser

ENGINE = os.path.join(os.path.dirname(__file__), "..", "retro-engine.py")


def _write(tmp_path, data):
    p = tmp_path / "batch-state.json"
    p.write_text(json.dumps(data), encoding="utf-8")
    return str(p)


def test_failures_extracted_and_completed_skipped(tmp_path):
    path = _write(
        tmp_path,
        {
            "status": "failed",
            "issueResults": [
                {
                    "issueNumber": 1,
                    "title": "ok",
                    "status": "completed",
                    "completedStages": [
                        "pipeline-start",
                        "issue-pickup",
                        "feature-planning",
                        "feature-dev",
                        "feature-validate",
                        "pr-create",
                        "pr-merge",
                        "pipeline-finish",
                    ],
                },
                {
                    "issueNumber": 2,
                    "title": "bad",
                    "status": "failed",
                    "completedStages": ["pipeline-start"],
                    "durationMs": 5,
                    "tokenUsage": {"cost_usd": 1.5},
                },
            ],
        },
    )
    out = BatchStateParser().parse_file(path)
    assert out["total_issues"] == 2
    assert [f["issue_number"] for f in out["failures"]] == [2]
    assert out["failures"][0]["failed_stages"][0] == "issue-pickup"
    assert out["failures"][0]["token_usage"] == {"cost_usd": 1.5}


def test_missing_or_malformed_returns_none(tmp_path):
    assert BatchStateParser().parse_file(str(tmp_path / "nope.json")) is None
    bad = tmp_path / "bad.json"
    bad.write_text("{", encoding="utf-8")
    assert BatchStateParser().parse_file(str(bad)) is None


def test_engine_help_imports():
    r = subprocess.run(
        [sys.executable, ENGINE, "--help"], capture_output=True, text=True
    )
    assert r.returncode == 0, r.stderr
