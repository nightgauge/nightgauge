"""
Unit tests for scripts/retro/classifiers/failure_classifier.py.

Tests cover all 7 classification categories, priority ordering, confidence
levels, and classify_all().
"""

from __future__ import annotations

import json
import os
import sys

# Make the retro scripts package importable regardless of CWD.
sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

import pytest

from classifiers.failure_classifier import (
    FailureClassifier,
    _COST_HEURISTIC_THRESHOLD_USD,
    _DURATION_HEURISTIC_THRESHOLD_MS,
)


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def make_event(
    issue: int = 1,
    log_signals: list[str] | None = None,
    failed_stages: list[str] | None = None,
    cost: float = 0.0,
    duration_ms: int = 0,
) -> dict:
    """Build a minimal failure event dict suitable for FailureClassifier.classify()."""
    return {
        "issue_number": issue,
        "title": f"Test issue #{issue}",
        "outcome": "failed",
        "failed_stages": failed_stages or [],
        "log_signals": log_signals or [],
        "errors": [],
        "estimated_cost_usd": cost,
        "total_duration_ms": duration_ms,
        "token_usage": {},
        "sources": ["test"],
    }


@pytest.fixture
def classifier() -> FailureClassifier:
    return FailureClassifier()


# ---------------------------------------------------------------------------
# Category classification tests
# ---------------------------------------------------------------------------


class TestBudgetExceeded:
    def test_classify_budget_exceeded(self, classifier: FailureClassifier) -> None:
        """Pattern 'budget exceeded' in log_signals triggers budget-exceeded."""
        event = make_event(log_signals=["budget exceeded during feature-dev stage"])
        result = classifier.classify(event)
        assert result["failure_category"] == "budget-exceeded"

    def test_classify_budget_exceeded_by_cost(
        self, classifier: FailureClassifier
    ) -> None:
        """An estimated cost above threshold triggers budget-exceeded via cost heuristic."""
        event = make_event(cost=_COST_HEURISTIC_THRESHOLD_USD + 5.0)
        result = classifier.classify(event)
        assert result["failure_category"] == "budget-exceeded"

    def test_cost_below_threshold_does_not_trigger_budget(
        self, classifier: FailureClassifier
    ) -> None:
        """Cost exactly at threshold does NOT trigger budget-exceeded (threshold is >)."""
        event = make_event(cost=_COST_HEURISTIC_THRESHOLD_USD)
        result = classifier.classify(event)
        # No pattern, no stage — should fall through to unknown
        assert result["failure_category"] == "unknown"

    def test_budget_pattern_token_limit(self, classifier: FailureClassifier) -> None:
        """Pattern 'token limit' also triggers budget-exceeded."""
        event = make_event(log_signals=["token limit reached"])
        result = classifier.classify(event)
        assert result["failure_category"] == "budget-exceeded"

    def test_budget_pattern_exceeds_limit(self, classifier: FailureClassifier) -> None:
        """Pattern 'exceeds hard limit' triggers budget-exceeded."""
        event = make_event(log_signals=["cost exceeds hard limit"])
        result = classifier.classify(event)
        assert result["failure_category"] == "budget-exceeded"


class TestTimeout:
    def test_classify_timeout(self, classifier: FailureClassifier) -> None:
        """Pattern 'timed out' in log_signals triggers timeout."""
        event = make_event(log_signals=["stage feature-dev timed out after 45 min"])
        result = classifier.classify(event)
        assert result["failure_category"] == "timeout"

    def test_classify_timeout_by_duration(self, classifier: FailureClassifier) -> None:
        """A total_duration_ms above the threshold triggers timeout heuristic."""
        event = make_event(duration_ms=_DURATION_HEURISTIC_THRESHOLD_MS + 100_000)
        result = classifier.classify(event)
        assert result["failure_category"] == "timeout"

    def test_duration_exactly_at_threshold_does_not_trigger(
        self, classifier: FailureClassifier
    ) -> None:
        """Duration exactly at the threshold does NOT trigger timeout (threshold is >)."""
        event = make_event(duration_ms=_DURATION_HEURISTIC_THRESHOLD_MS)
        result = classifier.classify(event)
        assert result["failure_category"] == "unknown"

    def test_timeout_pattern_keyword(self, classifier: FailureClassifier) -> None:
        """Bare keyword 'timeout' in log_signals triggers timeout."""
        event = make_event(log_signals=["connection timeout"])
        result = classifier.classify(event)
        assert result["failure_category"] == "timeout"


class TestCiInfrastructure:
    def test_classify_ci_infrastructure(self, classifier: FailureClassifier) -> None:
        """Pattern 'workflow failed' triggers ci-infrastructure."""
        event = make_event(log_signals=["workflow failed in CI"])
        result = classifier.classify(event)
        assert result["failure_category"] == "ci-infrastructure"

    def test_classify_ci_by_stage_pr_create(
        self, classifier: FailureClassifier
    ) -> None:
        """failed_stages=['pr-create'] triggers ci-infrastructure via stage rule."""
        event = make_event(failed_stages=["pr-create"])
        result = classifier.classify(event)
        assert result["failure_category"] == "ci-infrastructure"

    def test_classify_ci_by_stage_pr_merge(self, classifier: FailureClassifier) -> None:
        """failed_stages=['pr-merge'] triggers ci-infrastructure via stage rule."""
        event = make_event(failed_stages=["pr-merge"])
        result = classifier.classify(event)
        assert result["failure_category"] == "ci-infrastructure"

    def test_ci_pattern_github_actions(self, classifier: FailureClassifier) -> None:
        """Pattern 'github actions.*fail' triggers ci-infrastructure."""
        event = make_event(log_signals=["github actions workflow failed"])
        result = classifier.classify(event)
        assert result["failure_category"] == "ci-infrastructure"

    def test_ci_pattern_ci_checks_failed(self, classifier: FailureClassifier) -> None:
        """Pattern 'ci checks failed' triggers ci-infrastructure."""
        event = make_event(log_signals=["CI checks failed"])
        result = classifier.classify(event)
        assert result["failure_category"] == "ci-infrastructure"


class TestValidationFailure:
    def test_classify_validation_failure(self, classifier: FailureClassifier) -> None:
        """Pattern 'tests failed' triggers validation-failure."""
        event = make_event(log_signals=["tests failed: 4 failures"])
        result = classifier.classify(event)
        assert result["failure_category"] == "validation-failure"

    def test_classify_validation_by_stage(self, classifier: FailureClassifier) -> None:
        """failed_stages=['feature-validate'] triggers validation-failure via stage rule."""
        event = make_event(failed_stages=["feature-validate"])
        result = classifier.classify(event)
        assert result["failure_category"] == "validation-failure"

    def test_validation_pattern_tsc_error(self, classifier: FailureClassifier) -> None:
        """Pattern 'tsc.*error' triggers validation-failure."""
        event = make_event(log_signals=["tsc compile error in src/main.ts"])
        result = classifier.classify(event)
        assert result["failure_category"] == "validation-failure"

    def test_validation_pattern_build_failed(
        self, classifier: FailureClassifier
    ) -> None:
        """Pattern 'build failed' triggers validation-failure."""
        event = make_event(log_signals=["build failed with exit code 1"])
        result = classifier.classify(event)
        assert result["failure_category"] == "validation-failure"

    def test_validation_pattern_type_error(self, classifier: FailureClassifier) -> None:
        """Pattern 'type error' triggers validation-failure."""
        event = make_event(log_signals=["type error in module"])
        result = classifier.classify(event)
        assert result["failure_category"] == "validation-failure"


class TestStateManagement:
    def test_classify_state_management(self, classifier: FailureClassifier) -> None:
        """Pattern 'context file missing' triggers state-management."""
        event = make_event(log_signals=["context file missing at path /tmp/ctx.json"])
        result = classifier.classify(event)
        assert result["failure_category"] == "state-management"

    def test_state_management_pattern_json_parse_error(
        self, classifier: FailureClassifier
    ) -> None:
        """Pattern 'json.*parse.*error' triggers state-management."""
        event = make_event(log_signals=["json parse error at line 12"])
        result = classifier.classify(event)
        assert result["failure_category"] == "state-management"

    def test_state_management_pattern_failed_to_parse(
        self, classifier: FailureClassifier
    ) -> None:
        """Pattern 'failed to parse' triggers state-management."""
        event = make_event(log_signals=["failed to parse context handoff file"])
        result = classifier.classify(event)
        assert result["failure_category"] == "state-management"

    def test_state_management_pattern_schema_mismatch(
        self, classifier: FailureClassifier
    ) -> None:
        """Pattern 'schema mismatch' triggers state-management."""
        event = make_event(log_signals=["schema mismatch on handoff JSON"])
        result = classifier.classify(event)
        assert result["failure_category"] == "state-management"

    def test_state_management_pattern_handoff_file_not_found(
        self, classifier: FailureClassifier
    ) -> None:
        """Pattern 'handoff file.*not found' triggers state-management."""
        event = make_event(log_signals=["handoff file not found: ctx-1234.json"])
        result = classifier.classify(event)
        assert result["failure_category"] == "state-management"


class TestModelCapability:
    def test_classify_model_capability(self, classifier: FailureClassifier) -> None:
        """Pattern 'model returned empty' triggers model-capability."""
        event = make_event(log_signals=["model returned empty response on attempt 3"])
        result = classifier.classify(event)
        assert result["failure_category"] == "model-capability"

    def test_model_capability_pattern_reprompt_loop(
        self, classifier: FailureClassifier
    ) -> None:
        """Pattern 're-prompt loop' triggers model-capability."""
        event = make_event(log_signals=["detected re-prompt loop after 5 retries"])
        result = classifier.classify(event)
        assert result["failure_category"] == "model-capability"

    def test_model_capability_pattern_output_did_not_meet(
        self, classifier: FailureClassifier
    ) -> None:
        """Pattern 'output did not meet' triggers model-capability."""
        event = make_event(log_signals=["output did not meet schema requirements"])
        result = classifier.classify(event)
        assert result["failure_category"] == "model-capability"

    def test_model_capability_pattern_unexpected_output(
        self, classifier: FailureClassifier
    ) -> None:
        """Pattern 'unexpected.*output' triggers model-capability."""
        event = make_event(log_signals=["unexpected output from model"])
        result = classifier.classify(event)
        assert result["failure_category"] == "model-capability"


class TestUnknown:
    def test_classify_unknown(self, classifier: FailureClassifier) -> None:
        """An event with no matching signals or stages falls through to unknown."""
        event = make_event(log_signals=["some unrecognized message"])
        result = classifier.classify(event)
        assert result["failure_category"] == "unknown"

    def test_classify_unknown_empty_event(self, classifier: FailureClassifier) -> None:
        """An event with empty log_signals and no failed_stages is unknown."""
        event = make_event()
        result = classifier.classify(event)
        assert result["failure_category"] == "unknown"


# ---------------------------------------------------------------------------
# Priority ordering tests
# ---------------------------------------------------------------------------


class TestPriorityOrder:
    def test_priority_order_budget_over_validation(
        self, classifier: FailureClassifier
    ) -> None:
        """budget-exceeded takes priority over validation-failure (first-match-wins)."""
        event = make_event(log_signals=["budget exceeded", "tests failed"])
        result = classifier.classify(event)
        assert result["failure_category"] == "budget-exceeded"

    def test_priority_order_budget_over_timeout(
        self, classifier: FailureClassifier
    ) -> None:
        """budget-exceeded takes priority over timeout."""
        event = make_event(log_signals=["budget exceeded"], duration_ms=1_900_000)
        result = classifier.classify(event)
        assert result["failure_category"] == "budget-exceeded"

    def test_priority_order_timeout_over_ci(
        self, classifier: FailureClassifier
    ) -> None:
        """timeout takes priority over ci-infrastructure."""
        event = make_event(log_signals=["timed out", "workflow failed"])
        result = classifier.classify(event)
        assert result["failure_category"] == "timeout"

    def test_priority_order_ci_over_validation(
        self, classifier: FailureClassifier
    ) -> None:
        """ci-infrastructure takes priority over validation-failure."""
        event = make_event(log_signals=["workflow failed", "tests failed"])
        result = classifier.classify(event)
        assert result["failure_category"] == "ci-infrastructure"

    def test_priority_order_validation_over_state(
        self, classifier: FailureClassifier
    ) -> None:
        """validation-failure takes priority over state-management."""
        event = make_event(log_signals=["tests failed", "context file missing"])
        result = classifier.classify(event)
        assert result["failure_category"] == "validation-failure"

    def test_priority_order_state_over_model(
        self, classifier: FailureClassifier
    ) -> None:
        """state-management takes priority over model-capability."""
        event = make_event(log_signals=["context file missing", "model returned empty"])
        result = classifier.classify(event)
        assert result["failure_category"] == "state-management"


# ---------------------------------------------------------------------------
# classify_all() tests
# ---------------------------------------------------------------------------


class TestClassifyAll:
    def test_classify_all(self, classifier: FailureClassifier) -> None:
        """classify_all classifies a list of 3 events correctly in order."""
        events = [
            make_event(issue=1, log_signals=["budget exceeded"]),
            make_event(issue=2, log_signals=["tests failed"]),
            make_event(issue=3, failed_stages=["pr-create"]),
        ]
        results = classifier.classify_all(events)
        assert len(results) == 3
        assert results[0]["failure_category"] == "budget-exceeded"
        assert results[1]["failure_category"] == "validation-failure"
        assert results[2]["failure_category"] == "ci-infrastructure"

    def test_classify_all_empty(self, classifier: FailureClassifier) -> None:
        """classify_all returns an empty list for empty input."""
        assert classifier.classify_all([]) == []

    def test_classify_all_preserves_issue_numbers(
        self, classifier: FailureClassifier
    ) -> None:
        """classify_all result order matches input order (issue numbers are preserved)."""
        events = [make_event(issue=42), make_event(issue=99)]
        results = classifier.classify_all(events)
        assert results[0]["issue_number"] == 42
        assert results[1]["issue_number"] == 99


# ---------------------------------------------------------------------------
# Confidence level tests
# ---------------------------------------------------------------------------


class TestConfidence:
    def test_confidence_high(self, classifier: FailureClassifier) -> None:
        """Pattern match + stage match → confidence='high'."""
        # validation-failure has stage 'feature-validate' and pattern 'tests failed'
        event = make_event(
            log_signals=["tests failed"],
            failed_stages=["feature-validate"],
        )
        result = classifier.classify(event)
        assert result["failure_category"] == "validation-failure"
        assert result["confidence"] == "high"

    def test_confidence_medium_pattern_only(
        self, classifier: FailureClassifier
    ) -> None:
        """Pattern match without stage match → confidence='medium'."""
        # validation-failure pattern without the associated stage
        event = make_event(log_signals=["tests failed"])
        result = classifier.classify(event)
        assert result["failure_category"] == "validation-failure"
        assert result["confidence"] == "medium"

    def test_confidence_low_stage_only(self, classifier: FailureClassifier) -> None:
        """Stage match without a pattern match → confidence='low'."""
        # pr-create stage triggers ci-infrastructure but no pattern text present
        event = make_event(failed_stages=["pr-create"])
        result = classifier.classify(event)
        assert result["failure_category"] == "ci-infrastructure"
        assert result["confidence"] == "low"

    def test_confidence_low_cost_heuristic_only(
        self, classifier: FailureClassifier
    ) -> None:
        """Cost heuristic match (no pattern, no stage) → confidence='low'."""
        event = make_event(cost=_COST_HEURISTIC_THRESHOLD_USD + 10.0)
        result = classifier.classify(event)
        assert result["failure_category"] == "budget-exceeded"
        assert result["confidence"] == "low"

    def test_confidence_low_duration_heuristic_only(
        self, classifier: FailureClassifier
    ) -> None:
        """Duration heuristic match (no pattern, no stage) → confidence='low'."""
        event = make_event(duration_ms=_DURATION_HEURISTIC_THRESHOLD_MS + 200_000)
        result = classifier.classify(event)
        assert result["failure_category"] == "timeout"
        assert result["confidence"] == "low"

    def test_confidence_high_ci_pattern_and_stage(
        self, classifier: FailureClassifier
    ) -> None:
        """ci-infrastructure: pattern + matching stage → confidence='high'."""
        event = make_event(
            log_signals=["workflow failed"],
            failed_stages=["pr-merge"],
        )
        result = classifier.classify(event)
        assert result["failure_category"] == "ci-infrastructure"
        assert result["confidence"] == "high"


# ---------------------------------------------------------------------------
# Result shape / field presence tests
# ---------------------------------------------------------------------------


class TestResultShape:
    def test_classify_returns_all_required_keys(
        self, classifier: FailureClassifier
    ) -> None:
        """classify() result dict contains all documented output keys."""
        event = make_event(issue=7, log_signals=["tests failed"])
        result = classifier.classify(event)
        required_keys = {
            "issue_number",
            "title",
            "failure_stage",
            "failure_category",
            "confidence",
            "root_cause_summary",
            "evidence",
            "failed_stages",
            "estimated_cost_usd",
            "total_duration_ms",
        }
        assert required_keys.issubset(result.keys())

    def test_classify_preserves_issue_number(
        self, classifier: FailureClassifier
    ) -> None:
        """classify() result carries the issue_number from the input event."""
        event = make_event(issue=42)
        result = classifier.classify(event)
        assert result["issue_number"] == 42

    def test_classify_failure_stage_from_failed_stages(
        self, classifier: FailureClassifier
    ) -> None:
        """failure_stage is the first element of failed_stages."""
        event = make_event(
            failed_stages=["feature-dev", "feature-validate"],
            log_signals=["tests failed"],
        )
        result = classifier.classify(event)
        assert result["failure_stage"] == "feature-dev"

    def test_classify_failure_stage_unknown_when_no_stages(
        self, classifier: FailureClassifier
    ) -> None:
        """failure_stage is 'unknown' when failed_stages is empty."""
        event = make_event(log_signals=["tests failed"])
        result = classifier.classify(event)
        assert result["failure_stage"] == "unknown"

    def test_evidence_list_for_matching_event(
        self, classifier: FailureClassifier
    ) -> None:
        """evidence list contains the matching log signal string."""
        event = make_event(log_signals=["tests failed with 3 errors"])
        result = classifier.classify(event)
        assert any("tests failed" in ev for ev in result["evidence"])

    def test_evidence_empty_for_stage_only_match(
        self, classifier: FailureClassifier
    ) -> None:
        """For a stage-only match, evidence contains 'failed stage: <name>'."""
        event = make_event(failed_stages=["pr-create"])
        result = classifier.classify(event)
        assert any("pr-create" in ev for ev in result["evidence"])

    def test_root_cause_summary_is_nonempty_string(
        self, classifier: FailureClassifier
    ) -> None:
        """root_cause_summary is a non-empty string for every classification."""
        for log_signals, cost, duration_ms in [
            (["budget exceeded"], 0, 0),
            (["timed out"], 0, 0),
            (["workflow failed"], 0, 0),
            (["tests failed"], 0, 0),
            (["context file missing"], 0, 0),
            (["model returned empty"], 0, 0),
            ([], 0, 0),  # unknown
        ]:
            event = make_event(
                log_signals=log_signals, cost=cost, duration_ms=duration_ms
            )
            result = classifier.classify(event)
            assert isinstance(result["root_cause_summary"], str)
            assert len(result["root_cause_summary"]) > 0


# ---------------------------------------------------------------------------
# Edge-case / robustness tests
# ---------------------------------------------------------------------------


class TestEdgeCases:
    def test_case_insensitive_matching(self, classifier: FailureClassifier) -> None:
        """Pattern matching is case-insensitive."""
        event = make_event(log_signals=["BUDGET EXCEEDED"])
        result = classifier.classify(event)
        assert result["failure_category"] == "budget-exceeded"

    def test_mixed_case_timeout(self, classifier: FailureClassifier) -> None:
        """'Timed Out' (mixed case) matches timeout category."""
        event = make_event(log_signals=["Stage Timed Out"])
        result = classifier.classify(event)
        assert result["failure_category"] == "timeout"

    def test_log_signals_as_dicts(self, classifier: FailureClassifier) -> None:
        """log_signals entries that are dicts with a 'text' key are handled."""
        event = make_event()
        event["log_signals"] = [{"text": "context file missing at /tmp/ctx.json"}]
        result = classifier.classify(event)
        assert result["failure_category"] == "state-management"

    def test_errors_list_with_message_field(
        self, classifier: FailureClassifier
    ) -> None:
        """Evidence is extracted from 'errors' list dicts via 'message' key."""
        event = make_event()
        event["errors"] = [
            {"message": "model returned empty output", "stage": "feature-dev"}
        ]
        result = classifier.classify(event)
        assert result["failure_category"] == "model-capability"

    def test_top_level_error_string(self, classifier: FailureClassifier) -> None:
        """Top-level 'error' key string is included in evidence collection."""
        event = make_event()
        event["error"] = "json decode error reading handoff"
        result = classifier.classify(event)
        assert result["failure_category"] == "state-management"

    def test_nested_stages_error(self, classifier: FailureClassifier) -> None:
        """Stage-level errors in a 'stages' dict are included in evidence."""
        event = make_event()
        event["stages"] = {
            "feature-dev": {"error": "workflow failed in CI step", "duration_ms": 300}
        }
        result = classifier.classify(event)
        assert result["failure_category"] == "ci-infrastructure"

    def test_none_values_do_not_crash(self, classifier: FailureClassifier) -> None:
        """Explicit None values for optional fields do not raise exceptions."""
        event = {
            "issue_number": 1,
            "title": None,
            "outcome": "failed",
            "failed_stages": None,
            "log_signals": None,
            "errors": None,
            "estimated_cost_usd": None,
            "total_duration_ms": None,
            "token_usage": None,
            "sources": None,
        }
        result = classifier.classify(event)
        # No signals, no stages, no cost/duration → deterministically unknown
        assert result["failure_category"] == "unknown"

    def test_duplicate_log_signals_deduplication(
        self, classifier: FailureClassifier
    ) -> None:
        """Duplicate log signal strings are deduplicated in evidence collection."""
        event = make_event(log_signals=["tests failed", "tests failed", "tests failed"])
        result = classifier.classify(event)
        # evidence should not have duplicates
        assert result["evidence"].count("tests failed") == 1


# ---------------------------------------------------------------------------
# New pattern tests — budget-exceeded (context window / token exhaustion)
# ---------------------------------------------------------------------------


class TestBudgetExceededNewPatterns:
    def test_context_window_exceeded(self, classifier: FailureClassifier) -> None:
        """Pattern 'context window exceeded' triggers budget-exceeded."""
        event = make_event(log_signals=["context window exceeded at 200k tokens"])
        result = classifier.classify(event)
        assert result["failure_category"] == "budget-exceeded"

    def test_maximum_context_length(self, classifier: FailureClassifier) -> None:
        """Pattern 'maximum context length' triggers budget-exceeded."""
        event = make_event(log_signals=["maximum context length reached"])
        result = classifier.classify(event)
        assert result["failure_category"] == "budget-exceeded"

    def test_max_tokens_exceeded(self, classifier: FailureClassifier) -> None:
        """Pattern 'max tokens exceeded' triggers budget-exceeded."""
        event = make_event(log_signals=["max tokens exceeded on this request"])
        result = classifier.classify(event)
        assert result["failure_category"] == "budget-exceeded"

    def test_input_too_long(self, classifier: FailureClassifier) -> None:
        """Pattern 'input too long' triggers budget-exceeded."""
        event = make_event(log_signals=["input too long for model context"])
        result = classifier.classify(event)
        assert result["failure_category"] == "budget-exceeded"


# ---------------------------------------------------------------------------
# New pattern tests — timeout (error codes, extended signals)
# ---------------------------------------------------------------------------


class TestTimeoutNewPatterns:
    def test_classify_timeout_etimedout(self, classifier: FailureClassifier) -> None:
        """Error code ETIMEDOUT triggers timeout category."""
        event = make_event(log_signals=["connect ETIMEDOUT 192.168.1.1:443"])
        result = classifier.classify(event)
        assert result["failure_category"] == "timeout"

    def test_classify_timeout_deadline_exceeded(
        self, classifier: FailureClassifier
    ) -> None:
        """Pattern 'deadline exceeded' triggers timeout category."""
        event = make_event(log_signals=["deadline exceeded for stage feature-dev"])
        result = classifier.classify(event)
        assert result["failure_category"] == "timeout"

    def test_classify_timeout_operation_timed_out(
        self, classifier: FailureClassifier
    ) -> None:
        """Pattern 'operation timed out' triggers timeout category."""
        event = make_event(log_signals=["operation timed out after 60 seconds"])
        result = classifier.classify(event)
        assert result["failure_category"] == "timeout"

    def test_classify_timeout_took_too_long(
        self, classifier: FailureClassifier
    ) -> None:
        """Pattern 'took too long' triggers timeout category."""
        event = make_event(log_signals=["stage took too long to complete"])
        result = classifier.classify(event)
        assert result["failure_category"] == "timeout"


# ---------------------------------------------------------------------------
# New pattern tests — ci-infrastructure (network errors, HTTP codes)
# ---------------------------------------------------------------------------


class TestCiInfrastructureNewPatterns:
    def test_classify_ci_econnrefused(self, classifier: FailureClassifier) -> None:
        """Error code ECONNREFUSED triggers ci-infrastructure."""
        event = make_event(log_signals=["connect ECONNREFUSED 127.0.0.1:3000"])
        result = classifier.classify(event)
        assert result["failure_category"] == "ci-infrastructure"

    def test_classify_ci_enotfound(self, classifier: FailureClassifier) -> None:
        """Error code ENOTFOUND triggers ci-infrastructure."""
        event = make_event(log_signals=["getaddrinfo ENOTFOUND api.github.com"])
        result = classifier.classify(event)
        assert result["failure_category"] == "ci-infrastructure"

    def test_classify_ci_503(self, classifier: FailureClassifier) -> None:
        """HTTP 503 Service Unavailable triggers ci-infrastructure."""
        event = make_event(log_signals=["GitHub API returned 503 Service Unavailable"])
        result = classifier.classify(event)
        assert result["failure_category"] == "ci-infrastructure"

    def test_classify_ci_502(self, classifier: FailureClassifier) -> None:
        """HTTP 502 Bad Gateway triggers ci-infrastructure."""
        event = make_event(log_signals=["502 Bad Gateway from upstream server"])
        result = classifier.classify(event)
        assert result["failure_category"] == "ci-infrastructure"

    def test_classify_ci_rate_limit(self, classifier: FailureClassifier) -> None:
        """Pattern 'rate limit exceeded' triggers ci-infrastructure."""
        event = make_event(log_signals=["rate limit exceeded for GitHub API"])
        result = classifier.classify(event)
        assert result["failure_category"] == "ci-infrastructure"

    def test_classify_ci_secondary_rate_limit(
        self, classifier: FailureClassifier
    ) -> None:
        """Pattern 'secondary rate limit' triggers ci-infrastructure."""
        event = make_event(log_signals=["secondary rate limit triggered"])
        result = classifier.classify(event)
        assert result["failure_category"] == "ci-infrastructure"

    def test_classify_ci_runner_lost(self, classifier: FailureClassifier) -> None:
        """Pattern 'runner lost' triggers ci-infrastructure."""
        event = make_event(log_signals=["runner lost connection mid-job"])
        result = classifier.classify(event)
        assert result["failure_category"] == "ci-infrastructure"

    def test_classify_ci_network_error(self, classifier: FailureClassifier) -> None:
        """Pattern 'network error' triggers ci-infrastructure."""
        event = make_event(log_signals=["network error connecting to registry"])
        result = classifier.classify(event)
        assert result["failure_category"] == "ci-infrastructure"


# ---------------------------------------------------------------------------
# New pattern tests — validation-failure (JS errors, linting, exit codes)
# ---------------------------------------------------------------------------


class TestValidationFailureNewPatterns:
    def test_classify_validation_syntax_error(
        self, classifier: FailureClassifier
    ) -> None:
        """SyntaxError in logs triggers validation-failure."""
        event = make_event(log_signals=["SyntaxError: Unexpected token '<'"])
        result = classifier.classify(event)
        assert result["failure_category"] == "validation-failure"

    def test_classify_validation_reference_error(
        self, classifier: FailureClassifier
    ) -> None:
        """ReferenceError in logs triggers validation-failure."""
        event = make_event(log_signals=["ReferenceError: foo is not defined"])
        result = classifier.classify(event)
        assert result["failure_category"] == "validation-failure"

    def test_classify_validation_assertion_failed(
        self, classifier: FailureClassifier
    ) -> None:
        """Pattern 'assertion failed' triggers validation-failure."""
        event = make_event(log_signals=["assertion failed: expected 1 to equal 2"])
        result = classifier.classify(event)
        assert result["failure_category"] == "validation-failure"

    def test_classify_validation_eslint_error(
        self, classifier: FailureClassifier
    ) -> None:
        """Pattern 'eslint.*error' triggers validation-failure."""
        event = make_event(log_signals=["eslint: 5 errors found in 2 files"])
        result = classifier.classify(event)
        assert result["failure_category"] == "validation-failure"

    def test_classify_validation_n_errors_found(
        self, classifier: FailureClassifier
    ) -> None:
        """Pattern '3 errors found' triggers validation-failure."""
        event = make_event(log_signals=["3 errors found, 0 warnings"])
        result = classifier.classify(event)
        assert result["failure_category"] == "validation-failure"

    def test_classify_validation_exit_code(
        self, classifier: FailureClassifier
    ) -> None:
        """Pattern 'exit code 1' triggers validation-failure."""
        event = make_event(log_signals=["process exited with exit code 1"])
        result = classifier.classify(event)
        assert result["failure_category"] == "validation-failure"

    def test_classify_validation_vitest_fail(
        self, classifier: FailureClassifier
    ) -> None:
        """Pattern 'vitest.*fail' triggers validation-failure."""
        event = make_event(log_signals=["vitest: 2 tests failed"])
        result = classifier.classify(event)
        assert result["failure_category"] == "validation-failure"


# ---------------------------------------------------------------------------
# New pattern tests — state-management (file system errors, null access)
# ---------------------------------------------------------------------------


class TestStateManagementNewPatterns:
    def test_classify_state_management_enoent(
        self, classifier: FailureClassifier
    ) -> None:
        """Error code ENOENT triggers state-management."""
        event = make_event(
            log_signals=["ENOENT: no such file or directory, open '/tmp/ctx.json'"]
        )
        result = classifier.classify(event)
        assert result["failure_category"] == "state-management"

    def test_classify_state_management_eacces(
        self, classifier: FailureClassifier
    ) -> None:
        """Error code EACCES triggers state-management."""
        event = make_event(log_signals=["EACCES: permission denied '/etc/secret'"])
        result = classifier.classify(event)
        assert result["failure_category"] == "state-management"

    def test_classify_state_management_no_such_file(
        self, classifier: FailureClassifier
    ) -> None:
        """Pattern 'no such file or directory' triggers state-management."""
        event = make_event(log_signals=["no such file or directory: ctx.json"])
        result = classifier.classify(event)
        assert result["failure_category"] == "state-management"

    def test_classify_state_management_invalid_json(
        self, classifier: FailureClassifier
    ) -> None:
        """Pattern 'invalid json' triggers state-management."""
        event = make_event(log_signals=["invalid json in handoff file"])
        result = classifier.classify(event)
        assert result["failure_category"] == "state-management"

    def test_classify_state_management_cannot_read_property(
        self, classifier: FailureClassifier
    ) -> None:
        """Pattern 'cannot read propert' triggers state-management."""
        event = make_event(
            log_signals=["TypeError: cannot read properties of undefined (reading 'x')"]
        )
        result = classifier.classify(event)
        assert result["failure_category"] == "state-management"

    def test_classify_state_management_cannot_find_module(
        self, classifier: FailureClassifier
    ) -> None:
        """Pattern 'cannot find module' triggers state-management."""
        event = make_event(log_signals=["Error: cannot find module './missing-file'"])
        result = classifier.classify(event)
        assert result["failure_category"] == "state-management"


# ---------------------------------------------------------------------------
# New pattern tests — model-capability (empty responses, degradation)
# ---------------------------------------------------------------------------


class TestModelCapabilityNewPatterns:
    def test_classify_model_empty_response(
        self, classifier: FailureClassifier
    ) -> None:
        """Pattern 'empty response' triggers model-capability."""
        event = make_event(log_signals=["empty response from AI model"])
        result = classifier.classify(event)
        assert result["failure_category"] == "model-capability"

    def test_classify_model_truncated_output(
        self, classifier: FailureClassifier
    ) -> None:
        """Pattern 'truncated output' triggers model-capability."""
        event = make_event(log_signals=["truncated output received from model"])
        result = classifier.classify(event)
        assert result["failure_category"] == "model-capability"

    def test_classify_model_overloaded(self, classifier: FailureClassifier) -> None:
        """Pattern 'overloaded' triggers model-capability."""
        event = make_event(log_signals=["API overloaded, please retry"])
        result = classifier.classify(event)
        assert result["failure_category"] == "model-capability"

    def test_classify_model_malformed_response(
        self, classifier: FailureClassifier
    ) -> None:
        """Pattern 'malformed response' triggers model-capability."""
        event = make_event(log_signals=["malformed response from model"])
        result = classifier.classify(event)
        assert result["failure_category"] == "model-capability"


# ---------------------------------------------------------------------------
# _extract_stage_errors() unit tests
# ---------------------------------------------------------------------------


class TestExtractStageErrors:
    def test_extract_top_level_error(self, tmp_path: "Path") -> None:
        """Top-level 'error' field is extracted from context file."""

        ctx = {"error": "ENOENT: no such file or directory"}
        p = tmp_path / "dev-1.json"
        p.write_text(json.dumps(ctx))
        result = FailureClassifier._extract_stage_errors(str(p))
        assert any("ENOENT" in s for s in result)

    def test_extract_errors_list(self, tmp_path: "Path") -> None:
        """errors[] list with message+stage is extracted from context file."""

        ctx = {
            "errors": [{"message": "build failed", "stage": "feature-dev"}]
        }
        p = tmp_path / "dev-2.json"
        p.write_text(json.dumps(ctx))
        result = FailureClassifier._extract_stage_errors(str(p))
        assert any("build failed" in s for s in result)
        assert any("feature-dev" in s for s in result)

    def test_extract_build_verification_failure(self, tmp_path: "Path") -> None:
        """build_verification.status='failed' emits a synthetic error string."""

        ctx = {"build_verification": {"status": "failed", "ran": True}}
        p = tmp_path / "dev-3.json"
        p.write_text(json.dumps(ctx))
        result = FailureClassifier._extract_stage_errors(str(p))
        assert any("build" in s.lower() for s in result)

    def test_extract_tests_failed_count(self, tmp_path: "Path") -> None:
        """tests_status.failed > 0 emits a synthetic error string."""

        ctx = {"tests_status": {"passed": 3, "failed": 2}}
        p = tmp_path / "dev-4.json"
        p.write_text(json.dumps(ctx))
        result = FailureClassifier._extract_stage_errors(str(p))
        assert any("failed" in s.lower() for s in result)

    def test_extract_nonexistent_file_returns_empty(self) -> None:
        """Non-existent context file returns empty list, no exception."""
        result = FailureClassifier._extract_stage_errors("/nonexistent/path/dev-999.json")
        assert result == []

    def test_extract_invalid_json_returns_empty(self, tmp_path: "Path") -> None:
        """Malformed JSON in context file returns empty list, no exception."""
        p = tmp_path / "bad.json"
        p.write_text("{invalid json here}")
        result = FailureClassifier._extract_stage_errors(str(p))
        assert result == []

    def test_extract_no_errors_returns_empty(self, tmp_path: "Path") -> None:
        """Context file with no error fields returns empty list."""

        ctx = {"schema_version": "1.7", "issue_number": 42}
        p = tmp_path / "dev-5.json"
        p.write_text(json.dumps(ctx))
        result = FailureClassifier._extract_stage_errors(str(p))
        assert result == []


# ---------------------------------------------------------------------------
# _extract_error_codes() unit tests
# ---------------------------------------------------------------------------


class TestExtractErrorCodes:
    def test_extract_enoent(self) -> None:
        """ENOENT is extracted from an error string."""
        codes = FailureClassifier._extract_error_codes(
            "ENOENT: no such file or directory"
        )
        assert "ENOENT" in codes

    def test_extract_etimedout(self) -> None:
        """ETIMEDOUT is extracted from an error string."""
        codes = FailureClassifier._extract_error_codes(
            "connect ETIMEDOUT 10.0.0.1:3000"
        )
        assert "ETIMEDOUT" in codes

    def test_extract_multiple_codes(self) -> None:
        """Multiple error codes in one string are all extracted."""
        codes = FailureClassifier._extract_error_codes(
            "first ENOENT then EACCES on retry"
        )
        assert "ENOENT" in codes
        assert "EACCES" in codes

    def test_extract_no_codes_returns_empty_set(self) -> None:
        """String with no error codes returns empty set."""
        codes = FailureClassifier._extract_error_codes("build failed with exit code 1")
        assert codes == set()

    def test_extract_does_not_match_lowercase(self) -> None:
        """Lowercase 'enoent' is not matched (codes are uppercase by convention)."""
        codes = FailureClassifier._extract_error_codes("enoent: file missing")
        assert "enoent" not in codes


# ---------------------------------------------------------------------------
# Context file integration — classify() with context_files
# ---------------------------------------------------------------------------


class TestContextFilesIntegration:
    def test_classify_with_context_file_enoent(
        self, classifier: FailureClassifier, tmp_path: "Path"
    ) -> None:
        """ENOENT in a context file triggers state-management classification."""

        ctx = {"error": "ENOENT: no such file or directory '/tmp/missing.json'"}
        p = tmp_path / "dev-10.json"
        p.write_text(json.dumps(ctx))

        event = make_event(issue=10)
        event["context_files"] = [str(p)]
        result = classifier.classify(event)
        assert result["failure_category"] == "state-management"

    def test_classify_with_context_file_build_failed(
        self, classifier: FailureClassifier, tmp_path: "Path"
    ) -> None:
        """build_verification failed in context file triggers validation-failure."""

        ctx = {
            "build_verification": {"status": "failed", "ran": True},
            "errors": [{"message": "build failed with exit code 1"}],
        }
        p = tmp_path / "dev-11.json"
        p.write_text(json.dumps(ctx))

        event = make_event(issue=11)
        event["context_files"] = [str(p)]
        result = classifier.classify(event)
        assert result["failure_category"] == "validation-failure"

    def test_classify_skips_missing_context_file(
        self, classifier: FailureClassifier
    ) -> None:
        """Missing context file path does not raise — event is classified normally."""
        event = make_event(log_signals=["tests failed"])
        event["context_files"] = ["/nonexistent/ctx-999.json"]
        result = classifier.classify(event)
        # Should still classify based on log_signals
        assert result["failure_category"] == "validation-failure"
