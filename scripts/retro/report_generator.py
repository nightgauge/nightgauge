"""
report_generator.py — Generate JSON and Markdown retro reports from classified
failure data.

Consumes the output of the retro pipeline (classified + cross-referenced failure
events) and produces:
  - A structured JSON report conforming to schema_version "1.0"
  - A human-readable Markdown report suitable for terminal display or file output
  - A JSON file written to .nightgauge/pipeline/retro-report-YYYY-MM-DD.json

No external dependencies. Requires Python 3.8+.
"""

from __future__ import annotations

import json
import os
from collections import Counter
from datetime import datetime, timezone
from typing import Optional


# ---------------------------------------------------------------------------
# Category metadata — titles, recommendations, and effort estimates
# ---------------------------------------------------------------------------

_CATEGORY_META: dict[str, dict[str, str]] = {
    "budget-exceeded": {
        "title": "Budget exceeded terminating pipeline runs",
        "recommendation": (
            "Review and adjust budget limits for affected sizes/stages in config.yaml"
        ),
        "effort": "low",
    },
    "timeout": {
        "title": "Stage timeout terminating runs",
        "recommendation": ("Increase ci_timeout or investigate slow stages"),
        "effort": "medium",
    },
    "ci-infrastructure": {
        "title": "CI/infrastructure failures blocking PR stages",
        "recommendation": (
            "Investigate CI runner stability; consider retry configuration"
        ),
        "effort": "low",
    },
    "validation-failure": {
        "title": "Test/type-check failures blocking validation",
        "recommendation": ("Fix failing tests and type errors before retrying"),
        "effort": "medium",
    },
    "state-management": {
        "title": "Pipeline state/context file issues",
        "recommendation": ("Investigate context file generation in upstream stages"),
        "effort": "medium",
    },
    "model-capability": {
        "title": "AI model output quality issues",
        "recommendation": (
            "Consider model tier upgrade or prompt refinement for affected stages"
        ),
        "effort": "high",
    },
    "unknown": {
        "title": "Unclassified failures",
        "recommendation": (
            "Manual investigation required; check logs for additional context"
        ),
        "effort": "high",
    },
}

# Severity priority order (index 0 = highest) — used for sorting
_SEVERITY_ORDER = ["critical", "high", "medium", "low", "info"]

# Map severity to numeric priority weight (lower = more important)
_SEVERITY_PRIORITY: dict[str, int] = {
    severity: rank for rank, severity in enumerate(_SEVERITY_ORDER)
}


class ReportGenerator:
    """
    Generate JSON and Markdown pipeline retro reports from classified failure data.

    Usage::

        gen = ReportGenerator()
        report = gen.generate_json_report(scope, failures, findings)
        md = gen.generate_markdown_report(report)
        path = gen.write_json_report(report, output_dir)
    """

    # ------------------------------------------------------------------
    # Public API
    # ------------------------------------------------------------------

    def generate_json_report(
        self,
        scope: dict,
        failures: list[dict],
        findings: Optional[list[dict]] = None,
    ) -> dict:
        """Build the full JSON report structure.

        Parameters
        ----------
        scope:
            Scope descriptor dict conforming to the report schema. Expected
            keys: type, from, to, issue_filter, issues_analyzed,
            failure_count, data_sources.
        failures:
            List of classified + cross-referenced failure event dicts.  Each
            dict is expected to have at minimum: issue_number, failure_category,
            failed_stages, estimated_cost_usd, root_cause_summary.
        findings:
            Optional pre-built findings list. When omitted (None), findings
            are auto-generated via :meth:`build_findings`.

        Returns
        -------
        dict
            Complete report dict conforming to schema_version "1.0".
        """
        if findings is None:
            findings = self.build_findings(failures)

        recommendations = self.build_recommendations(findings)
        summary = self._compute_summary(scope, failures, findings)

        return {
            "schema_version": "1.0",
            "scope": scope,
            "summary": summary,
            "failures": failures,
            "findings": findings,
            "recommendations": recommendations,
            "created_at": datetime.now(timezone.utc).isoformat(),
        }

    def generate_markdown_report(self, report: dict) -> str:
        """Generate a human-readable Markdown report from a JSON report dict.

        Parameters
        ----------
        report:
            JSON report dict as produced by :meth:`generate_json_report`.

        Returns
        -------
        str
            Multi-line string containing the formatted report.
        """
        lines: list[str] = []

        scope = report.get("scope", {})
        summary = report.get("summary", {})
        findings = report.get("findings", [])
        recommendations = report.get("recommendations", [])

        # ----------------------------------------------------------------
        # Header
        # ----------------------------------------------------------------
        lines.append("PIPELINE RETRO REPORT")
        lines.append("=" * 55)
        lines.append("")

        # Scope line
        scope_line = self._format_scope_line(scope)
        lines.append(f"Scope:          {scope_line}")

        # Data sources
        data_sources = scope.get("data_sources") or []
        if data_sources:
            sources_label = self._format_data_sources(data_sources)
            lines.append(f"Data Sources:   {sources_label}")

        # Failure rate
        failure_rate = summary.get("failure_rate", 0.0)
        failure_count = scope.get("failure_count", 0)
        issues_analyzed = scope.get("issues_analyzed", 0)
        rate_pct = int(round(failure_rate * 100))
        lines.append(
            f"Failure Rate:   {rate_pct}% ({failure_count}/{issues_analyzed} issues)"
        )

        # Wasted cost
        wasted_cost = summary.get("total_wasted_cost_usd", 0.0)
        lines.append(f"Wasted Cost:    ${wasted_cost:.2f} across failed runs")

        lines.append("")

        # ----------------------------------------------------------------
        # Failure breakdown bar chart
        # ----------------------------------------------------------------
        failures_list = report.get("failures", [])
        if failures_list:
            lines.append("FAILURE BREAKDOWN BY CATEGORY")
            lines.append("-" * 55)

            category_groups = self._group_by_category(failures_list)
            max_count = max((len(v) for v in category_groups.values()), default=1)

            for category, cat_failures in sorted(
                category_groups.items(),
                key=lambda kv: len(kv[1]),
                reverse=True,
            ):
                count = len(cat_failures)
                bar = self._make_bar(count, max_count, bar_width=10)
                issue_nums = sorted(
                    {
                        f.get("issue_number")
                        for f in cat_failures
                        if f.get("issue_number") is not None
                    }
                )
                issue_label = self._format_issue_list(issue_nums)
                issue_word = "issue" if count == 1 else "issues"
                lines.append(
                    f"  {category:<26}{bar}  {count} {issue_word}  {issue_label}"
                )

            lines.append("")

            # Stage hotspot
            hotspot = summary.get("stage_hotspot")
            hotspot_appearances = self._count_stage_hotspot(failures_list, hotspot)
            if hotspot and hotspot != "unknown":
                lines.append(
                    f"STAGE HOTSPOT: {hotspot} "
                    f"(appears in {hotspot_appearances} of {failure_count} failures)"
                )
                lines.append("")

        # ----------------------------------------------------------------
        # Findings
        # ----------------------------------------------------------------
        if findings:
            lines.append("FINDINGS")
            lines.append("-" * 55)
            lines.append("")

            for finding in findings:
                severity = (finding.get("severity") or "unknown").upper()
                category = finding.get("category", "unknown")
                title = finding.get("title", "")
                affected = finding.get("affected_issues") or []
                root_cause = finding.get("root_cause", "")
                pattern = finding.get("pattern", "")
                recommendation = finding.get("recommendation", "")
                recurrence_risk = (
                    finding.get("estimated_recurrence_risk") or "unknown"
                ).upper()

                issue_label = self._format_issue_list(sorted(affected))

                lines.append(f"  [{severity}] {category}: {title}")
                lines.append(f"    Issues: {issue_label}")
                if root_cause:
                    lines.append(f"    Root Cause: {root_cause}")
                if pattern:
                    lines.append(f"    Pattern: {pattern}")
                if recommendation:
                    lines.append(f"    Recommendation: {recommendation}")
                lines.append(f"    Recurrence risk: {recurrence_risk}")
                lines.append("")

        # ----------------------------------------------------------------
        # Recommendations
        # ----------------------------------------------------------------
        if recommendations:
            lines.append("RECOMMENDATIONS (sorted by impact)")
            lines.append("-" * 55)

            for rec in recommendations:
                priority = rec.get("priority", "?")
                action = rec.get("action", "")
                impact = rec.get("impact", "")
                effort = rec.get("effort", "unknown")
                lines.append(f"  {priority}. {action} ({impact}, {effort} effort)")

            lines.append("")

        # ----------------------------------------------------------------
        # Footer — report file path
        # ----------------------------------------------------------------
        lines.append("-" * 55)
        report_date = self._extract_report_date(report)
        report_path = f".nightgauge/pipeline/retro-report-{report_date}.json"
        lines.append(f"Report saved: {report_path}")

        return "\n".join(lines)

    def write_json_report(self, report: dict, output_dir: str) -> str:
        """Write a JSON report to disk and return the file path.

        Writes to ``<output_dir>/retro-report-YYYY-MM-DD.json``.  The output
        directory is created if it does not exist.

        Parameters
        ----------
        report:
            JSON report dict as produced by :meth:`generate_json_report`.
        output_dir:
            Directory path to write the file into.  Typically
            ``.nightgauge/pipeline``.

        Returns
        -------
        str
            Absolute (or relative, matching *output_dir*) path to the written
            file.
        """
        report_date = self._extract_report_date(report)
        filename = f"retro-report-{report_date}.json"
        os.makedirs(output_dir, exist_ok=True)
        filepath = os.path.join(output_dir, filename)

        with open(filepath, "w", encoding="utf-8") as fh:
            json.dump(report, fh, indent=2, ensure_ascii=False)

        return filepath

    def build_findings(self, failures: list[dict]) -> list[dict]:
        """Aggregate individual failure events into category-level findings.

        Groups failures by ``failure_category``, computes severity based on
        count and percentage share, and assembles a human-readable finding dict
        for each category.

        Parameters
        ----------
        failures:
            List of classified failure event dicts.  Each dict should have:
            failure_category, issue_number, root_cause_summary, failed_stages,
            estimated_cost_usd.

        Returns
        -------
        list[dict]
            List of finding dicts, sorted by severity (critical first) then
            by count descending.
        """
        if not failures:
            return []

        total_failures = len(failures)
        groups: dict[str, list[dict]] = {}
        for failure in failures:
            category = failure.get("failure_category") or "unknown"
            groups.setdefault(category, []).append(failure)

        findings: list[dict] = []
        for category, group in groups.items():
            count = len(group)
            percentage = count / total_failures if total_failures > 0 else 0.0

            severity = self._compute_severity(count, percentage)

            affected_issues = sorted(
                {
                    f.get("issue_number")
                    for f in group
                    if f.get("issue_number") is not None
                }
            )

            # Aggregate root cause summaries
            root_cause_parts = []
            seen_causes: set[str] = set()
            for f in group:
                rc = (f.get("root_cause_summary") or "").strip()
                if rc and rc not in seen_causes:
                    seen_causes.add(rc)
                    root_cause_parts.append(rc)
            root_cause = " | ".join(root_cause_parts) if root_cause_parts else ""

            # Build pattern description
            pattern = self._build_pattern(group, category)

            meta = _CATEGORY_META.get(category, _CATEGORY_META["unknown"])
            title = meta["title"]
            recommendation = meta["recommendation"]

            recurrence_risk = self._compute_recurrence_risk(count, percentage, severity)

            findings.append(
                {
                    "category": category,
                    "severity": severity,
                    "count": count,
                    "affected_issues": affected_issues,
                    "title": title,
                    "root_cause": root_cause,
                    "pattern": pattern,
                    "recommendation": recommendation,
                    "estimated_recurrence_risk": recurrence_risk,
                }
            )

        # Sort: severity order first, then count descending
        findings.sort(
            key=lambda f: (
                _SEVERITY_PRIORITY.get(f["severity"], 99),
                -f["count"],
            )
        )

        return findings

    def build_recommendations(self, findings: list[dict]) -> list[dict]:
        """Generate prioritized recommendations from findings.

        Each finding produces one recommendation. Recommendations are sorted
        by priority (1 = highest), determined by severity then count.

        Parameters
        ----------
        findings:
            List of finding dicts as produced by :meth:`build_findings`.

        Returns
        -------
        list[dict]
            List of recommendation dicts with keys: priority, action,
            impact, effort, category.
        """
        if not findings:
            return []

        # Findings are expected to already be sorted by severity + count from
        # build_findings, but re-sort defensively.
        sorted_findings = sorted(
            findings,
            key=lambda f: (
                _SEVERITY_PRIORITY.get(f.get("severity") or "low", 99),
                -(f.get("count") or 0),
            ),
        )

        recommendations: list[dict] = []
        for rank, finding in enumerate(sorted_findings, start=1):
            category = finding.get("category") or "unknown"
            count = finding.get("count") or 0
            meta = _CATEGORY_META.get(category, _CATEGORY_META["unknown"])

            action = self._build_action(finding, meta)
            impact = (
                f"Eliminates {count} recurring failure"
                if count == 1
                else f"Eliminates {count} recurring failures"
            )

            recommendations.append(
                {
                    "priority": rank,
                    "action": action,
                    "impact": impact,
                    "effort": meta["effort"],
                    "category": category,
                }
            )

        return recommendations

    # ------------------------------------------------------------------
    # Private — summary computation
    # ------------------------------------------------------------------

    @staticmethod
    def _compute_summary(
        scope: dict, failures: list[dict], findings: list[dict]
    ) -> dict:
        """Compute the summary block for the report."""
        issues_analyzed = scope.get("issues_analyzed") or 0
        failure_count = scope.get("failure_count") or len(failures)

        failure_rate = failure_count / issues_analyzed if issues_analyzed > 0 else 0.0

        total_wasted_cost = sum(f.get("estimated_cost_usd") or 0.0 for f in failures)

        # Most common category
        category_counts: Counter = Counter(
            f.get("failure_category") or "unknown" for f in failures
        )
        most_common_category: Optional[str] = None
        if category_counts:
            most_common_category = category_counts.most_common(1)[0][0]

        # Stage hotspot — stage appearing most across all failures' failed_stages
        stage_counts: Counter = Counter()
        for f in failures:
            for stage in f.get("failed_stages") or []:
                stage_counts[stage] += 1
        stage_hotspot: Optional[str] = None
        if stage_counts:
            stage_hotspot = stage_counts.most_common(1)[0][0]

        # Unique categories ordered by frequency
        categories_found = [cat for cat, _ in category_counts.most_common()]

        return {
            "failure_rate": round(failure_rate, 4),
            "total_wasted_cost_usd": round(total_wasted_cost, 2),
            "most_common_category": most_common_category,
            "stage_hotspot": stage_hotspot,
            "categories_found": categories_found,
        }

    # ------------------------------------------------------------------
    # Private — findings helpers
    # ------------------------------------------------------------------

    @staticmethod
    def _compute_severity(count: int, percentage: float) -> str:
        """Determine severity level from count and failure percentage.

        Rules applied in order (count-based thresholds evaluate first):
          critical  — 3+ issues with same category  OR  >50% of failure pool
          high      — 2+ issues with same category  OR  >25% of failure pool
          medium    — single failure with any classified category
          low       — single failure, ambiguous / unknown category
          info      — no actual failures (count == 0)

        The count-based thresholds for 2+ issues cap at "high" even when the
        percentage exceeds 50%, preventing two failures in a small pool of two
        from being incorrectly escalated to critical.  For count == 1 the
        percentage thresholds apply normally against the failure pool share.
        """
        if count == 0:
            return "info"
        # Count-based thresholds (strict): 3+ issues → critical, 2+ → high.
        # These cap escalation at "high" for count == 2 regardless of percentage
        # to avoid spurious critical from a small (e.g. 2-failure) total pool.
        if count >= 3:
            return "critical"
        if count >= 2:
            return "high"
        # count == 1: apply percentage-of-failure-pool thresholds.
        if percentage > 0.5:
            return "critical"
        if percentage > 0.25:
            return "high"
        return "medium"

    @staticmethod
    def _compute_recurrence_risk(count: int, percentage: float, severity: str) -> str:
        """Estimate recurrence risk from severity and count."""
        if severity in ("critical", "high"):
            return "high"
        if severity == "medium":
            return "medium"
        return "low"

    @staticmethod
    def _build_pattern(group: list[dict], category: str) -> str:
        """Build a concise pattern description for a finding group."""
        if len(group) == 1:
            failure = group[0]
            stage = (
                failure.get("failure_stage")
                or (failure.get("failed_stages") or ["unknown"])[0]
            )
            issue = failure.get("issue_number", "?")
            return f"Single occurrence in issue #{issue} at stage {stage}"

        # Multiple failures — look for common stage
        stage_counts: Counter = Counter()
        for f in group:
            for stage in f.get("failed_stages") or []:
                stage_counts[stage] += 1
        if stage_counts:
            common_stage, common_count = stage_counts.most_common(1)[0]
            return (
                f"Recurring pattern: {common_count} of {len(group)} failures "
                f"involve stage {common_stage}"
            )

        return f"Pattern seen across {len(group)} issues with category {category}"

    @staticmethod
    def _build_action(finding: dict, meta: dict) -> str:
        """Build the recommendation action string for a finding."""
        title = finding.get("title") or meta.get("title", "Address failure")
        category = finding.get("category") or "unknown"
        affected = finding.get("affected_issues") or []
        issue_str = (
            ", ".join(f"#{n}" for n in affected) if affected else "affected issues"
        )
        return f"{meta['recommendation']} (affects {issue_str})"

    # ------------------------------------------------------------------
    # Private — markdown helpers
    # ------------------------------------------------------------------

    @staticmethod
    def _format_scope_line(scope: dict) -> str:
        """Produce a compact one-line scope description."""
        scope_type = scope.get("type") or "unknown"
        from_date = scope.get("from") or ""
        to_date = scope.get("to") or ""
        issues_analyzed = scope.get("issues_analyzed") or 0
        issue_filter = scope.get("issue_filter")

        if issue_filter is not None:
            return f"Single issue #{issue_filter} ({from_date})"

        if scope_type == "batch":
            date_part = from_date or to_date
            return f"Last batch run ({date_part}, {issues_analyzed} issues)"

        if scope_type == "date-range":
            if from_date and to_date:
                return f"Date range {from_date} to {to_date} ({issues_analyzed} issues)"
            date_part = from_date or to_date
            return f"Since {date_part} ({issues_analyzed} issues)"

        return f"{scope_type} ({issues_analyzed} issues)"

    @staticmethod
    def _format_data_sources(data_sources: list[str]) -> str:
        """Format data sources list as a readable string."""
        label_map = {
            "batch-state": "batch-state",
            "history": "execution history",
            "logs": "session logs",
        }
        parts = [label_map.get(s, s) for s in data_sources]
        return ", ".join(parts)

    @staticmethod
    def _format_issue_list(issue_nums: list) -> str:
        """Format a list of issue numbers as (#42, #43) string."""
        if not issue_nums:
            return "(none)"
        return "(" + ", ".join(f"#{n}" for n in issue_nums) + ")"

    @staticmethod
    def _make_bar(count: int, max_count: int, bar_width: int = 10) -> str:
        """Produce an ASCII bar scaled to bar_width characters.

        Uses '#' for filled characters and spaces for the remainder. The bar
        is wrapped in '[' and ']' brackets.

        Parameters
        ----------
        count:
            The value to represent.
        max_count:
            The maximum value (determines full bar).
        bar_width:
            Total number of characters in the bar body (default 10).

        Returns
        -------
        str
            A string like ``[##########]`` or ``[#####     ]``.
        """
        if max_count <= 0:
            filled = 0
        else:
            filled = round((count / max_count) * bar_width)
        filled = max(0, min(filled, bar_width))
        bar_body = "#" * filled + " " * (bar_width - filled)
        return f"[{bar_body}]"

    @staticmethod
    def _group_by_category(failures: list[dict]) -> dict[str, list[dict]]:
        """Group failures by failure_category."""
        groups: dict[str, list[dict]] = {}
        for f in failures:
            cat = f.get("failure_category") or "unknown"
            groups.setdefault(cat, []).append(f)
        return groups

    @staticmethod
    def _count_stage_hotspot(failures: list[dict], hotspot: Optional[str]) -> int:
        """Count how many failures include the hotspot stage."""
        if not hotspot:
            return 0
        count = 0
        for f in failures:
            if hotspot in (f.get("failed_stages") or []):
                count += 1
        return count

    @staticmethod
    def _extract_report_date(report: dict) -> str:
        """Extract a YYYY-MM-DD date string from the report for use in filenames.

        Prefers scope.from, then scope.to, then today's date.
        """
        scope = report.get("scope") or {}
        date_str = scope.get("from") or scope.get("to") or ""
        if date_str and len(date_str) >= 10:
            return date_str[:10]
        # Fall back to created_at
        created_at = report.get("created_at") or ""
        if created_at and len(created_at) >= 10:
            return created_at[:10]
        return datetime.now(timezone.utc).strftime("%Y-%m-%d")
