"""
Failure classifier for the nightgauge-retro pipeline analysis skill.

Implements deterministic, rule-based failure classification with first-match-wins
priority ordering. Classification rules are derived from the failure category
definitions in skills/nightgauge-retro/SKILL.md, Phase 4.

Categories (in priority order):
  1. budget-exceeded    -- token/cost budget exhausted
  2. timeout            -- stage or pipeline exceeded time limit
  3. ci-infrastructure  -- external CI system failure
  4. validation-failure -- tests, tsc, or build blocked progression
  5. state-management   -- context file missing or JSON parse error
  6. model-capability   -- AI model produced unusable output
  7. unknown            -- no other category matched

No external dependencies. Requires Python 3.8+.
"""

from __future__ import annotations

import json
import re
import sys
from pathlib import Path
from typing import Any


# ---------------------------------------------------------------------------
# Thresholds (named constants — avoid magic numbers scattered in logic)
# ---------------------------------------------------------------------------

#: Cost heuristic: flag as budget-exceeded when estimated cost exceeds this
#: USD amount and no pattern match was found. Callers with peer context
#: should pre-filter and set a tighter threshold; this is a standalone fallback.
_COST_HEURISTIC_THRESHOLD_USD: float = 10.0

#: Duration heuristic: flag as timeout when total_duration_ms exceeds this
#: value (30 minutes). The ideal threshold is 3x stage median, which requires
#: the full event list; this absolute fallback is used when running standalone.
_DURATION_HEURISTIC_THRESHOLD_MS: int = 1_800_000

#: Maximum characters of an evidence string to include in root-cause summaries.
_MAX_EVIDENCE_DETAIL_LEN: int = 120


# ---------------------------------------------------------------------------
# Classification rule definitions (priority order — first match wins)
# ---------------------------------------------------------------------------

# Each rule is a dict with:
#   category   : str           -- output category label
#   patterns   : list[str]     -- regex patterns tested against combined evidence text
#   stages     : list[str]     -- failed stages that strengthen / trigger a match
#   requires_stage : bool      -- True → stage match alone is sufficient (no pattern needed)
#   cost_check : bool          -- True → also check cost anomaly heuristic
#   duration_check : bool      -- True → also check 3x-median duration heuristic

_RULES: list[dict[str, Any]] = [
    {
        "category": "budget-exceeded",
        "patterns": [
            # Original patterns
            r"budget\s+exceeded",
            r"token\s+limit",
            r"costusd\s*[><=]+\s*budget",
            r"exceeds\s+(hard\s+)?limit",
            r"cost\s+\$[\d.]+\s+exceeds",
            # Context window / token exhaustion
            r"context\s+(window|length)\s+(exceeded|full|too\s+long|limit)",
            r"maximum\s+context\s+(length|window)",
            r"context\s+window\s+exceeded",
            r"max\s+tokens?\s+(exceeded|reached|limit)",
            r"input\s+too\s+long",
            r"prompt\s+too\s+long",
        ],
        "stages": [],
        "requires_stage": False,
        "cost_check": True,
        "duration_check": False,
    },
    {
        "category": "timeout",
        "patterns": [
            # Original patterns
            r"timed\s+out",
            r"timeout",
            r"exceeded\s+(ci_)?timeout",
            r"stage\s+duration\s*[><=]+\s*max",
            r"stage\s+\w[\w-]*\s+timed\s+out",
            # Error codes and extended timeout signals
            r"ETIMEDOUT",
            r"deadline\s+exceeded",
            r"operation\s+timed?\s+out",
            r"request\s+timed?\s+out",
            r"took\s+too\s+long",
            r"execution\s+time\s+limit",
            r"slow\s+runner",
        ],
        "stages": [],
        "requires_stage": False,
        "cost_check": False,
        "duration_check": True,
    },
    {
        "category": "ci-infrastructure",
        "patterns": [
            # Original patterns
            r"ci.*fail",
            r"workflow.*fail",
            r"gh\s+run\s+watch",
            r"ci\s+checks?\s+failed",
            r"github\s+actions.*fail",
            r"workflow\s+run\s+failed",
            # Network / HTTP infrastructure errors
            r"ECONNREFUSED",
            r"ENOTFOUND",
            r"connect\s+ECONNREFUSED",
            r"getaddrinfo\s+(ENOTFOUND|failed)",
            r"\b(502|503|504)\b",
            r"Bad\s+Gateway",
            r"Service\s+Unavailable",
            r"Gateway\s+Timeout",
            # GitHub runner / job failures
            r"runner\s+(lost|offline|unavailable)",
            r"job\s+(was\s+)?cancelled",
            r"network\s+(error|failure|unreachable)",
            # GitHub API rate limits
            r"rate\s+limit\s+(exceeded|reached)",
            r"secondary\s+rate\s+limit",
            r"api\s+rate\s+limit",
        ],
        "stages": ["pr-create", "pr-merge"],
        "requires_stage": False,
        "cost_check": False,
        "duration_check": False,
    },
    {
        "category": "validation-failure",
        "patterns": [
            # Original patterns
            r"tests?\s+failed",
            r"tsc.*error",
            r"build\s+failed",
            r"\d+\s+test\s+failures?",
            r"type\s+error",
            r"compilation\s+error",
            # JavaScript / TypeScript runtime errors
            r"\bSyntaxError\b",
            r"\bReferenceError\b",
            r"\bAssertionError\b",
            r"assertion\s+(failed|error)",
            # Linting and code quality failures
            r"linting?\s+(error|failed)",
            r"eslint.*error",
            r"\d+\s+errors?\s+found",
            r"failed\s+to\s+compile",
            # Test runner specific
            r"vitest.*fail",
            r"jest.*fail",
            r"pytest.*fail",
            r"FAIL\s+\S+\.test\.",
            # Non-zero exit codes
            r"exit\s+code\s+[1-9]\d*",
        ],
        "stages": ["feature-validate"],
        "requires_stage": False,
        "cost_check": False,
        "duration_check": False,
    },
    {
        "category": "state-management",
        "patterns": [
            # Original patterns
            r"context\s+file.*missing",
            r"json.*parse.*error",
            r"failed\s+to\s+parse",
            r"json\s+decode",
            r"missing\s+context\s+file",
            r"handoff\s+file.*not\s+found",
            r"schema\s+mismatch",
            # File system error codes
            r"\bENOENT\b",
            r"\bEACCES\b",
            r"no\s+such\s+file\s+or\s+directory",
            r"file\s+not\s+found",
            r"cannot\s+find\s+(module|file|path)",
            # JSON / parse errors
            r"unexpected\s+(end\s+of\s+)?json",
            r"invalid\s+json",
            r"malformed\s+json",
            # Null/undefined property access (broken context)
            r"cannot\s+read\s+propert",
            r"undefined\s+is\s+not\s+an?\s+object",
            r"null\s+reference",
            r"pipeline\s+state.*corrupt",
        ],
        "stages": [],
        "requires_stage": False,
        "cost_check": False,
        "duration_check": False,
    },
    {
        "category": "model-capability",
        "patterns": [
            # Original patterns
            r"model\s+returned\s+empty",
            r"unexpected.*output",
            r"re-?prompt\s+loop",
            r"unexpected\s+output\s+format",
            r"repeated\s+re-?prompt",
            r"output\s+did\s+not\s+meet",
            # Empty / truncated model responses
            r"empty\s+(response|output|reply)",
            r"truncated\s+output",
            r"malformed\s+(response|output)",
            r"model\s+output\s+(was\s+)?empty",
            r"no\s+output\s+(from\s+)?model",
            r"response\s+was\s+(empty|null|undefined)",
            # Model service degradation
            r"\boverloaded\b",
            r"stopped\s+(due\s+to|at)\s+max\s+tokens",
        ],
        "stages": [],
        "requires_stage": False,
        "cost_check": False,
        "duration_check": False,
    },
]


class FailureClassifier:
    """
    Rule-based failure event classifier.

    Applies classification rules in priority order (first-match-wins) to a
    unified failure event dict produced by the retro parsers. Returns a
    classified result dict with category, confidence level, root-cause summary,
    and matching evidence strings.

    Usage::

        classifier = FailureClassifier()
        result = classifier.classify(failure_event)
        results = classifier.classify_all(failure_events)

    The failure event dict may optionally include a ``context_files`` key
    containing a list of pipeline context file paths. When present, the
    classifier reads each file and supplements evidence with any error
    strings found inside them.
    """

    # ------------------------------------------------------------------
    # Public API
    # ------------------------------------------------------------------

    def classify(self, failure_event: dict) -> dict:
        """Classify a single failure event.

        Parameters
        ----------
        failure_event:
            Unified failure event dict as produced by the retro parsers.
            Expected keys: issue_number, title, outcome, failed_stages,
            log_signals, errors, estimated_cost_usd, total_duration_ms,
            token_usage, sources.

            Optional keys:
            - ``context_files``: list of str paths to pipeline context JSON
              files. If provided, each file is read and its error fields are
              added to the evidence collection.

        Returns
        -------
        dict
            Classified result with keys: issue_number, title, failure_stage,
            failure_category, confidence, root_cause_summary, evidence,
            failed_stages, estimated_cost_usd, total_duration_ms.
        """
        evidence_strings = self._collect_evidence_strings(failure_event)
        evidence_blob = "\n".join(evidence_strings).lower()

        failed_stages: list[str] = failure_event.get("failed_stages") or []
        failure_stage = failed_stages[0] if failed_stages else "unknown"

        estimated_cost = failure_event.get("estimated_cost_usd") or 0.0
        total_duration_ms = failure_event.get("total_duration_ms") or 0

        category, confidence, matched_evidence = self._apply_rules(
            evidence_blob=evidence_blob,
            evidence_strings=evidence_strings,
            failed_stages=failed_stages,
            estimated_cost=estimated_cost,
            total_duration_ms=total_duration_ms,
        )

        root_cause_summary = self._build_root_cause_summary(
            category=category,
            failure_stage=failure_stage,
            failed_stages=failed_stages,
            matched_evidence=matched_evidence,
            failure_event=failure_event,
        )

        return {
            "issue_number": failure_event.get("issue_number"),
            "title": failure_event.get("title", ""),
            "failure_stage": failure_stage,
            "failure_category": category,
            "confidence": confidence,
            "root_cause_summary": root_cause_summary,
            "evidence": matched_evidence,
            "failed_stages": failed_stages,
            "estimated_cost_usd": estimated_cost,
            "total_duration_ms": total_duration_ms,
        }

    def classify_all(self, failure_events: list[dict]) -> list[dict]:
        """Classify a list of failure events.

        Parameters
        ----------
        failure_events:
            List of unified failure event dicts.

        Returns
        -------
        list[dict]
            List of classified result dicts in the same order.
        """
        return [self.classify(event) for event in failure_events]

    # ------------------------------------------------------------------
    # Internal helpers
    # ------------------------------------------------------------------

    @staticmethod
    def _collect_evidence_strings(failure_event: dict) -> list[str]:
        """Gather all human-readable evidence strings from a failure event.

        Pulls from:
        - ``log_signals`` list entries (strings)
        - ``errors`` list entries — uses the ``message`` field
        - Top-level ``error`` string (history stage format)
        - Stage-level error strings nested in a ``stages`` dict
        - Pipeline context files listed in ``context_files`` (if present)

        Returns a deduplicated list preserving first-seen insertion order.
        """
        seen: set[str] = set()
        result: list[str] = []

        def _add(text: str) -> None:
            stripped = text.strip()
            if stripped and stripped not in seen:
                seen.add(stripped)
                result.append(stripped)

        # log_signals: list of plain strings
        for entry in failure_event.get("log_signals") or []:
            if isinstance(entry, str):
                _add(entry)
            elif isinstance(entry, dict):
                # Some parsers may emit {"line": N, "text": "..."}
                _add(entry.get("text") or entry.get("message") or "")

        # errors: list of dicts with at least a "message" key
        for err in failure_event.get("errors") or []:
            if isinstance(err, dict):
                _add(err.get("message") or "")
                # Also capture stage context if present
                stage = err.get("stage") or ""
                msg = err.get("message") or ""
                if stage and msg:
                    _add(f"[stage:{stage}] {msg}")
            elif isinstance(err, str):
                _add(err)

        # Top-level "error" string (history stage format)
        top_error = failure_event.get("error")
        if isinstance(top_error, str):
            _add(top_error)

        # Stage-level errors from a nested "stages" dict (history run format)
        stages = failure_event.get("stages") or {}
        for stage_name, stage_data in stages.items():
            if isinstance(stage_data, dict):
                err_msg = stage_data.get("error")
                if err_msg:
                    _add(f"[stage:{stage_name}] {err_msg}")

        # Pipeline context files — read error fields from JSON files on disk
        for path_str in failure_event.get("context_files") or []:
            try:
                extracted = FailureClassifier._extract_stage_errors(path_str)
                for s in extracted:
                    _add(s)
            except Exception as exc:
                print(
                    f"WARNING: FailureClassifier: could not read context file "
                    f"{path_str!r}: {exc}",
                    file=sys.stderr,
                )

        return result

    @staticmethod
    def _extract_stage_errors(context_file_path: str) -> list[str]:
        """Extract error strings from a pipeline context JSON file.

        Reads the JSON file at *context_file_path* and returns a list of
        error-bearing strings extracted from common error fields. Supports
        the following fields:

        - ``error``: top-level error string
        - ``errors``: list of dicts with ``message`` / ``stage`` keys
        - ``build_verification.status`` == ``"failed"``
        - ``tests_status.failed`` > 0

        Parameters
        ----------
        context_file_path:
            Absolute or relative path to a pipeline context JSON file (e.g.
            ``.nightgauge/pipeline/dev-42.json``).

        Returns
        -------
        list[str]
            Deduplicated list of error strings found in the file. Returns an
            empty list when the file cannot be read or contains no errors.
        """
        path = Path(context_file_path)
        if not path.is_file():
            return []

        try:
            data: dict = json.loads(path.read_text(encoding="utf-8"))
        except (json.JSONDecodeError, OSError):
            return []

        seen: set[str] = set()
        result: list[str] = []

        def _add(text: str) -> None:
            stripped = text.strip()
            if stripped and stripped not in seen:
                seen.add(stripped)
                result.append(stripped)

        # Top-level error string
        top_error = data.get("error")
        if isinstance(top_error, str):
            _add(top_error)

        # errors list
        for err in data.get("errors") or []:
            if isinstance(err, dict):
                msg = err.get("message") or ""
                stage = err.get("stage") or ""
                if msg:
                    _add(msg)
                    if stage:
                        _add(f"[stage:{stage}] {msg}")
            elif isinstance(err, str):
                _add(err)

        # build_verification failures
        build_ver = data.get("build_verification") or {}
        if isinstance(build_ver, dict) and build_ver.get("status") == "failed":
            _add("build_verification: build failed")

        # tests_status failures
        tests_status = data.get("tests_status") or {}
        if isinstance(tests_status, dict):
            failed_count = tests_status.get("failed")
            if isinstance(failed_count, int) and failed_count > 0:
                _add(f"tests_status: {failed_count} test(s) failed")

        # stage-level errors in nested stages dict
        stages = data.get("stages") or {}
        if isinstance(stages, dict):
            for stage_name, stage_data in stages.items():
                if isinstance(stage_data, dict):
                    err_msg = stage_data.get("error")
                    if err_msg:
                        _add(f"[stage:{stage_name}] {err_msg}")

        return result

    @staticmethod
    def _extract_error_codes(error_text: str) -> set[str]:
        """Extract POSIX / Node.js error code tokens from an error string.

        Looks for uppercase error code identifiers of the form ``E[A-Z]+``
        (e.g. ``ENOENT``, ``ETIMEDOUT``, ``ECONNREFUSED``) and returns them
        as a set of uppercase strings.

        **Note**: This method is a standalone public utility and is NOT called
        by the internal classification pipeline. Error codes are matched
        directly via regex patterns in ``_RULES`` (e.g. ``r"\\bENOENT\\b"``).
        Use this method externally when you need to enumerate error codes from
        a raw error string for logging, reporting, or pre-processing.

        Parameters
        ----------
        error_text:
            The raw error string to scan.

        Returns
        -------
        set[str]
            Set of matched error code tokens (uppercased). Empty when none
            are found.
        """
        return set(re.findall(r"\bE[A-Z]{2,}\b", error_text))

    @staticmethod
    def _match_patterns(patterns: list[str], text: str) -> list[str]:
        """Return the subset of ``patterns`` that match ``text`` (case-insensitive).

        Returns a list of matching pattern strings (not the matched substrings).
        ``text`` is expected to already be lowercased.
        """
        matched: list[str] = []
        for pat in patterns:
            if re.search(pat, text, re.IGNORECASE):
                matched.append(pat)
        return matched

    @staticmethod
    def _extract_matching_evidence(
        patterns: list[str], evidence_strings: list[str]
    ) -> list[str]:
        """Return evidence strings that match at least one of the given patterns."""
        matched: list[str] = []
        for ev in evidence_strings:
            for pat in patterns:
                if re.search(pat, ev, re.IGNORECASE):
                    matched.append(ev)
                    break
        return matched

    def _apply_rules(
        self,
        evidence_blob: str,
        evidence_strings: list[str],
        failed_stages: list[str],
        estimated_cost: float,
        total_duration_ms: float,
    ) -> tuple[str, str, list[str]]:
        """Apply classification rules in priority order.

        Returns (category, confidence, matched_evidence_strings).
        """
        failed_stages_set = set(failed_stages)

        for rule in _RULES:
            category: str = rule["category"]
            patterns: list[str] = rule["patterns"]
            rule_stages: list[str] = rule["stages"]

            pattern_match = self._match_patterns(patterns, evidence_blob)
            stage_match = bool(failed_stages_set & set(rule_stages))

            # Special heuristic: cost anomaly for budget-exceeded
            cost_triggered = False
            if rule.get("cost_check") and estimated_cost > 0:
                cost_triggered = estimated_cost > _COST_HEURISTIC_THRESHOLD_USD

            # Duration heuristic for timeout (3x median cannot be computed here
            # without the full event list; use absolute threshold as fallback)
            duration_triggered = False
            if rule.get("duration_check") and total_duration_ms > 0:
                duration_triggered = total_duration_ms > _DURATION_HEURISTIC_THRESHOLD_MS

            matched = bool(pattern_match or cost_triggered or duration_triggered)

            if not matched and not stage_match:
                continue

            # Determine confidence
            if pattern_match and stage_match:
                confidence = "high"
            elif pattern_match:
                confidence = "medium"
            else:
                # Stage-only or heuristic-only match
                confidence = "low"

            matched_evidence = self._extract_matching_evidence(
                patterns, evidence_strings
            )

            # Supplement evidence with stage context when stage triggered
            if stage_match and not matched_evidence:
                for s in failed_stages:
                    if s in rule_stages:
                        matched_evidence.append(f"failed stage: {s}")

            return category, confidence, matched_evidence

        # No rule matched → unknown
        return "unknown", "low", []

    @staticmethod
    def _build_root_cause_summary(
        category: str,
        failure_stage: str,
        failed_stages: list[str],
        matched_evidence: list[str],
        failure_event: dict,
    ) -> str:
        """Produce a concise, human-readable root-cause summary string."""
        issue = failure_event.get("issue_number", "?")
        cost = failure_event.get("estimated_cost_usd") or 0.0
        duration_ms = failure_event.get("total_duration_ms") or 0
        duration_min = round(duration_ms / 60000, 1) if duration_ms else 0

        # Use the first matching evidence line as a detail anchor when available
        detail = matched_evidence[0] if matched_evidence else ""
        # Truncate long detail lines
        if len(detail) > _MAX_EVIDENCE_DETAIL_LEN:
            detail = detail[: _MAX_EVIDENCE_DETAIL_LEN - 3] + "..."

        if category == "budget-exceeded":
            if cost:
                base = f"Issue #{issue} cost ${cost:.2f} exceeded budget"
            else:
                base = f"Issue #{issue} terminated due to budget/token limit"
            return (
                f"{base}. Stage: {failure_stage}. Evidence: {detail}"
                if detail
                else base
            )

        if category == "timeout":
            if duration_min:
                base = f"Issue #{issue} timed out after {duration_min} min"
            else:
                base = f"Issue #{issue} exceeded time limit"
            return (
                f"{base}. Stage: {failure_stage}. Evidence: {detail}"
                if detail
                else base
            )

        if category == "ci-infrastructure":
            base = f"CI/infrastructure failure in issue #{issue}"
            stages_str = ", ".join(failed_stages) if failed_stages else failure_stage
            return (
                f"{base}. Failed stage(s): {stages_str}. Evidence: {detail}"
                if detail
                else f"{base}. Failed stage(s): {stages_str}"
            )

        if category == "validation-failure":
            base = f"Tests, build, or TypeScript check failed for issue #{issue}"
            return (
                f"{base}. Stage: {failure_stage}. Evidence: {detail}"
                if detail
                else base
            )

        if category == "state-management":
            base = f"Context file missing or JSON parse error for issue #{issue}"
            return (
                f"{base}. Stage: {failure_stage}. Evidence: {detail}"
                if detail
                else base
            )

        if category == "model-capability":
            base = f"Model produced empty or unexpected output for issue #{issue}"
            return (
                f"{base}. Stage: {failure_stage}. Evidence: {detail}"
                if detail
                else base
            )

        # unknown
        base = f"Failure for issue #{issue} could not be classified"
        stages_str = ", ".join(failed_stages) if failed_stages else failure_stage
        return f"{base}. Failed stage(s): {stages_str}"
