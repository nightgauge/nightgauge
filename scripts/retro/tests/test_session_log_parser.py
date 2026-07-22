"""
test_session_log_parser.py — Unit tests for SessionLogParser.

Run with:
    pytest scripts/retro/tests/test_session_log_parser.py
"""

import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from parsers.session_log_parser import SessionLogParser

# ---------------------------------------------------------------------------
# Fixtures / helpers
# ---------------------------------------------------------------------------

FIXTURES_DIR = os.path.join(os.path.dirname(__file__), "fixtures")
SAMPLE_LOG = os.path.join(FIXTURES_DIR, "sample_session.log")


# ---------------------------------------------------------------------------
# 1. Filename parsing — with issue number
# ---------------------------------------------------------------------------


def test_parse_filename_with_issue(tmp_path):
    """Parsing 2026-02-15_42_session.log yields date=2026-02-15, issue_number=42."""
    log_file = tmp_path / "2026-02-15_42_session.log"
    log_file.write_text("")  # empty file is fine for filename extraction

    parser = SessionLogParser()
    result = parser.parse_file(str(log_file))

    assert result["date"] == "2026-02-15"
    assert result["issue_number"] == 42


# ---------------------------------------------------------------------------
# 2. Filename parsing — without issue number
# ---------------------------------------------------------------------------


def test_parse_filename_without_issue(tmp_path):
    """Parsing 2026-02-15_session.log yields date=2026-02-15, issue_number=None."""
    log_file = tmp_path / "2026-02-15_session.log"
    log_file.write_text("")

    parser = SessionLogParser()
    result = parser.parse_file(str(log_file))

    assert result["date"] == "2026-02-15"
    assert result["issue_number"] is None


# ---------------------------------------------------------------------------
# 3. Empty file
# ---------------------------------------------------------------------------


def test_parse_empty_file(tmp_path):
    """Empty file returns an empty structure with total_lines=0."""
    log_file = tmp_path / "2026-02-15_99_session.log"
    log_file.write_text("")

    parser = SessionLogParser()
    result = parser.parse_file(str(log_file))

    assert result["total_lines"] == 0
    assert result["stage_transitions"] == []
    assert result["budget_warnings"] == []
    assert result["budget_exceeded"] == []
    assert result["errors"] == []
    assert result["final_status"] == "unknown"
    assert result["token_usage"] == {}


# ---------------------------------------------------------------------------
# 4. Stage transitions
# ---------------------------------------------------------------------------


def test_parse_stage_transitions():
    """Sample log contains stage transitions with model and effort info."""
    parser = SessionLogParser()
    result = parser.parse_file(SAMPLE_LOG)

    transitions = result["stage_transitions"]
    assert len(transitions) > 0

    # Every entry must have the required keys.
    for t in transitions:
        assert "timestamp" in t
        assert "stage" in t
        assert "model" in t
        assert "effort" in t

    # Spot-check the first transition: issue-pickup with haiku/low.
    first = transitions[0]
    assert first["stage"] == "issue-pickup"
    assert first["model"] == "haiku"
    assert first["effort"] == "low"

    # Spot-check a later transition: feature-planning with sonnet/medium.
    planning_transitions = [t for t in transitions if t["stage"] == "feature-planning"]
    assert len(planning_transitions) > 0
    assert any(t["model"] == "sonnet" for t in planning_transitions)

    # Spot-check a high-effort transition: feature-dev with opus/high.
    dev_high = [
        t for t in transitions if t["stage"] == "feature-dev" and t["effort"] == "high"
    ]
    assert len(dev_high) >= 1
    assert dev_high[0]["model"] == "opus"


# ---------------------------------------------------------------------------
# 5. Budget warnings
# ---------------------------------------------------------------------------


def test_parse_budget_warnings():
    """Sample log captures budget warning events with cost_usd."""
    parser = SessionLogParser()
    result = parser.parse_file(SAMPLE_LOG)

    warnings = result["budget_warnings"]
    assert len(warnings) >= 1

    # All entries must carry cost_usd.
    for w in warnings:
        assert "timestamp" in w
        assert "message" in w
        assert "cost_usd" in w
        assert w["cost_usd"] is not None

    # The fixture's warning line mentions $6.50.
    warning_costs = [w["cost_usd"] for w in warnings]
    assert 6.50 in warning_costs


# ---------------------------------------------------------------------------
# 6. Budget exceeded
# ---------------------------------------------------------------------------


def test_parse_budget_exceeded():
    """Sample log captures budget exceeded events with cost_usd and limit_usd."""
    parser = SessionLogParser()
    result = parser.parse_file(SAMPLE_LOG)

    exceeded = result["budget_exceeded"]
    assert len(exceeded) >= 1

    # All entries must carry cost_usd; limit_usd may be None if only one amount
    # is present, but at least the primary exceeded event must have both.
    for e in exceeded:
        assert "timestamp" in e
        assert "message" in e
        assert "cost_usd" in e
        assert "limit_usd" in e

    # The fixture's hard-limit line: cost=$12.50, limit=$8.00.
    primary = next(
        (e for e in exceeded if e["cost_usd"] == 12.50),
        None,
    )
    assert primary is not None, "Expected an exceeded entry with cost_usd=12.50"
    assert primary["limit_usd"] == 8.00


# ---------------------------------------------------------------------------
# 7. Error events
# ---------------------------------------------------------------------------


def test_parse_errors():
    """Sample log captures error events with context lines."""
    parser = SessionLogParser()
    result = parser.parse_file(SAMPLE_LOG)

    errors = result["errors"]
    assert len(errors) >= 1

    # Each error entry must have the required shape.
    for e in errors:
        assert "timestamp" in e
        assert "stage" in e
        assert "message" in e
        assert "context" in e
        assert isinstance(e["context"], list)

    # Fixture contains errors from feature-validate (tests failed, tsc error,
    # stage failed).
    validate_errors = [e for e in errors if e["stage"] == "feature-validate"]
    assert len(validate_errors) >= 1


# ---------------------------------------------------------------------------
# 8. Final status — failure
# ---------------------------------------------------------------------------


def test_final_status_failure():
    """Parsing the sample log (which ends with pipeline-finish errors) yields failure."""
    parser = SessionLogParser()
    result = parser.parse_file(SAMPLE_LOG)

    # The sample fixture's last session ends with "Pipeline finished with errors"
    # which sets last_status to "failure".
    assert result["final_status"] == "failure"


# ---------------------------------------------------------------------------
# 9. Final status — success
# ---------------------------------------------------------------------------


def test_final_status_success(tmp_path):
    """A log ending with 'completed successfully' yields final_status='success'."""
    log_file = tmp_path / "2026-02-15_50_session.log"
    log_file.write_text(
        "[2026-02-15T09:00:00.000Z] [INFO] [pipeline-start] [#50] Pipeline started\n"
        "[2026-02-15T09:00:01.000Z] [INFO] [issue-pickup] [#50] Stage: issue-pickup Model: haiku Effort: low\n"
        "[2026-02-15T09:01:00.000Z] [INFO] [issue-pickup] [#50] Issue pickup complete\n"
        "[2026-02-15T09:05:00.000Z] [INFO] [pipeline-finish] [#50] Pipeline completed successfully\n"
    )

    parser = SessionLogParser()
    result = parser.parse_file(str(log_file))

    assert result["final_status"] == "success"


# ---------------------------------------------------------------------------
# 10. parse_directory — date filter
# ---------------------------------------------------------------------------


def test_parse_directory_with_date_filter(tmp_path):
    """Only files whose date is >= since_date are included."""
    # Create three log files spanning different dates.
    (tmp_path / "2026-02-10_1_session.log").write_text(
        "[2026-02-10T10:00:00.000Z] [INFO] [pipeline-finish] [#1] Pipeline completed successfully\n"
    )
    (tmp_path / "2026-02-15_2_session.log").write_text(
        "[2026-02-15T10:00:00.000Z] [INFO] [pipeline-finish] [#2] Pipeline completed successfully\n"
    )
    (tmp_path / "2026-02-20_3_session.log").write_text(
        "[2026-02-20T10:00:00.000Z] [INFO] [pipeline-finish] [#3] Pipeline completed successfully\n"
    )

    parser = SessionLogParser()
    results = parser.parse_directory(str(tmp_path), since_date="2026-02-15")

    assert len(results) == 2
    returned_dates = {r["date"] for r in results}
    assert "2026-02-15" in returned_dates
    assert "2026-02-20" in returned_dates
    assert "2026-02-10" not in returned_dates


# ---------------------------------------------------------------------------
# 11. parse_directory — issue filter
# ---------------------------------------------------------------------------


def test_parse_directory_with_issue_filter(tmp_path):
    """Only files matching the issue_filter are included; no-issue files are excluded."""
    (tmp_path / "2026-02-15_42_session.log").write_text(
        "[2026-02-15T10:00:00.000Z] [INFO] [pipeline-finish] [#42] Pipeline completed successfully\n"
    )
    (tmp_path / "2026-02-15_99_session.log").write_text(
        "[2026-02-15T10:01:00.000Z] [INFO] [pipeline-finish] [#99] Pipeline completed successfully\n"
    )
    # A no-issue file — should be excluded when issue_filter is set.
    (tmp_path / "2026-02-15_session.log").write_text(
        "[2026-02-15T10:02:00.000Z] [INFO] [pipeline-finish] Pipeline completed successfully\n"
    )

    parser = SessionLogParser()
    results = parser.parse_directory(str(tmp_path), issue_filter=42)

    assert len(results) == 1
    assert results[0]["issue_number"] == 42


# ---------------------------------------------------------------------------
# 12. parse_directory — non-existent directory
# ---------------------------------------------------------------------------


def test_parse_directory_nonexistent():
    """Passing a non-existent directory path returns an empty list."""
    parser = SessionLogParser()
    results = parser.parse_directory("/nonexistent/path/to/logs")

    assert results == []
