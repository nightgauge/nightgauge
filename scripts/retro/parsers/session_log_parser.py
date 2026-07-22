"""
session_log_parser.py — Parser for per-issue pipeline session log files.

Log format (canonical source):
    packages/nightgauge-vscode/src/utils/log-file-writer.ts

Each line follows the pattern:
    [ISO-8601] [LEVEL] [STAGE] [#N] MESSAGE

Log filename formats:
    YYYY-MM-DD_NNN_session.log   (with issue number)
    YYYY-MM-DD_session.log       (without issue number)

No external dependencies — stdlib only.
"""

import os
import re
from typing import Optional

# ---------------------------------------------------------------------------
# Compiled regular expressions
# ---------------------------------------------------------------------------

# Base line parser: captures timestamp, level, optional stage, optional issue
# number, and the remainder as message.
_RE_BASE_LINE = re.compile(r"^\[(.*?)\] \[(.*?)\] (?:\[(.*?)\] )?(?:\[#(\d+)\] )?(.*)$")

# Stage transition: INFO line containing Stage/Model/Effort fields.
_RE_STAGE_TRANSITION = re.compile(
    r"\[.*?\] \[INFO\] \[.*?\] \[#\d+\] .*Stage:\s+(\S+).*Model:\s+(\w+).*Effort:\s+(\w+)"
)

# Budget warning: WARN or WARNING line mentioning budget/cost with a dollar amount.
_RE_BUDGET_WARNING = re.compile(r"\[.*?\] \[WARN(?:ING)?\] .*(?:budget|cost).*\$[\d.]+")

# Budget exceeded: ERROR line mentioning budget exceeded or stage terminated due to budget.
_RE_BUDGET_EXCEEDED = re.compile(
    r"\[.*?\] \[ERROR\] .*(?:budget exceeded|terminated.*budget)",
    re.IGNORECASE,
)

# Stage failure: ERROR or FAIL level line mentioning failed/error.
_RE_STAGE_FAILURE = re.compile(
    r"\[.*?\] \[(?:ERROR|FAIL)\] .*(?:failed|error)",
    re.IGNORECASE,
)

# Successful completion marker.
_RE_COMPLETION = re.compile(
    r"\[.*?\] \[INFO\] .*(?:completed successfully|pipeline-finish)",
    re.IGNORECASE,
)

# Dollar amount extractor used for cost/limit parsing.
_RE_DOLLAR = re.compile(r"\$([\d.]+)")

# Token summary line — optional; matches lines that mention token counts.
_RE_TOKEN_SUMMARY = re.compile(
    r"(?:tokens?|token_usage|input_tokens|output_tokens)[:\s]+(\d+)",
    re.IGNORECASE,
)

# Filename pattern: YYYY-MM-DD_NNN_session.log or YYYY-MM-DD_session.log
_RE_FILENAME_WITH_ISSUE = re.compile(r"^(\d{4}-\d{2}-\d{2})_(\d+)_session\.log$")
_RE_FILENAME_WITHOUT_ISSUE = re.compile(r"^(\d{4}-\d{2}-\d{2})_session\.log$")


# ---------------------------------------------------------------------------
# Internal helpers
# ---------------------------------------------------------------------------


def _parse_filename(filename: str) -> tuple[Optional[str], Optional[int]]:
    """
    Extract (date_str, issue_number) from a session log filename.

    Returns (date_str, issue_number) where issue_number may be None.
    Returns (None, None) if the filename does not match either known format.
    """
    basename = os.path.basename(filename)

    match = _RE_FILENAME_WITH_ISSUE.match(basename)
    if match:
        return match.group(1), int(match.group(2))

    match = _RE_FILENAME_WITHOUT_ISSUE.match(basename)
    if match:
        return match.group(1), None

    return None, None


def _parse_line(raw_line: str) -> Optional[dict]:
    """
    Parse a single raw log line into its constituent fields.

    Returns a dict with keys: timestamp, level, stage, issue_number, message.
    Returns None if the line does not match the base log format.
    """
    line = raw_line.rstrip("\n\r")
    match = _RE_BASE_LINE.match(line)
    if not match:
        return None

    timestamp, level, stage, issue_str, message = match.groups()
    return {
        "timestamp": timestamp,
        "level": level.upper() if level else level,
        "stage": stage,
        "issue_number": int(issue_str) if issue_str else None,
        "message": message,
        "raw": line,
    }


def _extract_costs(message: str) -> list[float]:
    """
    Return all dollar amounts found in *message* as a list of floats.
    """
    return [float(v) for v in _RE_DOLLAR.findall(message)]


def _extract_token_info(message: str) -> dict:
    """
    Extract token counts from a message string.

    Returns a dict mapping token field names to integer counts.
    Returns an empty dict when no token information is found.
    """
    token_info: dict = {}
    for match in _RE_TOKEN_SUMMARY.finditer(message):
        # Use the text immediately before the colon/space as the key label.
        start = max(0, match.start() - 20)
        prefix = message[start : match.start()].split()
        key = prefix[-1].rstrip(":").lower() if prefix else "tokens"
        token_info[key] = int(match.group(1))
    return token_info


# ---------------------------------------------------------------------------
# Public API
# ---------------------------------------------------------------------------


class SessionLogParser:
    """
    Parser for Nightgauge pipeline session log files.

    Canonical log format is defined in:
        packages/nightgauge-vscode/src/utils/log-file-writer.ts

    Log line format:
        [ISO-8601] [LEVEL] [STAGE] [#N] MESSAGE

    Filename formats:
        YYYY-MM-DD_NNN_session.log   — session with an associated issue
        YYYY-MM-DD_session.log       — session without a specific issue
    """

    # ------------------------------------------------------------------
    # parse_file
    # ------------------------------------------------------------------

    def parse_file(self, filepath: str) -> dict:
        """
        Parse a single session log file and return structured data.

        Parameters
        ----------
        filepath : str
            Absolute or relative path to the session log file.

        Returns
        -------
        dict
            Structured representation of the session log with keys:

            log_file          — basename of the log file
            issue_number      — int extracted from filename, or None
            date              — YYYY-MM-DD string extracted from filename, or None
            stage_transitions — list of dicts: timestamp, stage, model, effort
            budget_warnings   — list of dicts: timestamp, stage, message, cost_usd
            budget_exceeded   — list of dicts: timestamp, stage, message,
                                cost_usd, limit_usd
            errors            — list of dicts: timestamp, stage, message, context
            final_status      — "success" | "failure" | "unknown"
            token_usage       — dict of token field -> int (may be empty)
            total_lines       — total number of lines in the file
        """
        filename = os.path.basename(filepath)
        date_str, issue_number = _parse_filename(filename)

        result: dict = {
            "log_file": filename,
            "issue_number": issue_number,
            "date": date_str,
            "stage_transitions": [],
            "budget_warnings": [],
            "budget_exceeded": [],
            "errors": [],
            "final_status": "unknown",
            "token_usage": {},
            "total_lines": 0,
        }

        try:
            with open(filepath, "r", encoding="utf-8", errors="replace") as fh:
                raw_lines = fh.readlines()
        except OSError:
            # File unreadable — return the empty structure as-is.
            return result

        if not raw_lines:
            return result

        result["total_lines"] = len(raw_lines)

        # Track the last meaningful status to infer final_status.
        last_status: str = "unknown"

        for idx, raw_line in enumerate(raw_lines):
            parsed = _parse_line(raw_line)
            if parsed is None:
                # Non-matching line (multi-line tail, blank, etc.) — skip.
                continue

            ts = parsed["timestamp"]
            level = parsed["level"]
            stage = parsed["stage"]
            message = parsed["message"]
            raw = parsed["raw"]

            # ----------------------------------------------------------------
            # Stage transitions
            # ----------------------------------------------------------------
            transition_match = _RE_STAGE_TRANSITION.match(raw)
            if transition_match:
                result["stage_transitions"].append(
                    {
                        "timestamp": ts,
                        "stage": transition_match.group(1),
                        "model": transition_match.group(2),
                        "effort": transition_match.group(3),
                    }
                )
                continue  # Transition lines are INFO; no need for further checks.

            # ----------------------------------------------------------------
            # Budget exceeded (check before budget warning — both can match ERROR)
            # ----------------------------------------------------------------
            if _RE_BUDGET_EXCEEDED.match(raw):
                costs = _extract_costs(message)
                cost_usd = costs[0] if len(costs) >= 1 else None
                limit_usd = costs[1] if len(costs) >= 2 else None
                result["budget_exceeded"].append(
                    {
                        "timestamp": ts,
                        "stage": stage,
                        "message": message,
                        "cost_usd": cost_usd,
                        "limit_usd": limit_usd,
                    }
                )
                last_status = "failure"
                continue

            # ----------------------------------------------------------------
            # Budget warnings
            # ----------------------------------------------------------------
            if _RE_BUDGET_WARNING.match(raw):
                costs = _extract_costs(message)
                result["budget_warnings"].append(
                    {
                        "timestamp": ts,
                        "stage": stage,
                        "message": message,
                        "cost_usd": costs[0] if costs else None,
                    }
                )
                continue

            # ----------------------------------------------------------------
            # General errors / stage failures
            # ----------------------------------------------------------------
            if level in ("ERROR", "FAIL") and _RE_STAGE_FAILURE.match(raw):
                context_lines = self._extract_context(raw_lines, idx, window=2)
                result["errors"].append(
                    {
                        "timestamp": ts,
                        "stage": stage,
                        "message": message,
                        "context": context_lines,
                    }
                )
                last_status = "failure"
                continue

            # ----------------------------------------------------------------
            # Completion / pipeline-finish
            # ----------------------------------------------------------------
            if _RE_COMPLETION.match(raw):
                if "completed successfully" in message.lower():
                    last_status = "success"
                elif "pipeline-finish" in (stage or "").lower():
                    # pipeline-finish with errors still appears in INFO; only
                    # mark success when the message explicitly says so.
                    if "error" in message.lower() or "fail" in message.lower():
                        last_status = "failure"
                    else:
                        last_status = "success"
                continue

            # ----------------------------------------------------------------
            # Token usage (optional — parsed from any matching line)
            # ----------------------------------------------------------------
            if "token" in message.lower():
                token_info = _extract_token_info(message)
                if token_info:
                    result["token_usage"].update(token_info)

        result["final_status"] = last_status
        return result

    # ------------------------------------------------------------------
    # parse_directory
    # ------------------------------------------------------------------

    def parse_directory(
        self,
        logs_dir: str,
        since_date: Optional[str] = None,
        issue_filter: Optional[int] = None,
    ) -> list:
        """
        Parse all matching session log files in *logs_dir*.

        Parameters
        ----------
        logs_dir : str
            Directory containing ``*_session.log`` files.
        since_date : str, optional
            ISO date string (``YYYY-MM-DD``).  Files with a date component
            strictly before this value are skipped.
        issue_filter : int, optional
            When provided, only files whose issue number matches this value
            are included.  Files without an issue number in their filename
            are excluded when an *issue_filter* is supplied.

        Returns
        -------
        list[dict]
            List of structured dicts as returned by :meth:`parse_file`,
            ordered by filename (alphabetically ascending, which corresponds
            to chronological order given the date-prefixed naming convention).
        """
        results: list = []

        try:
            entries = sorted(os.listdir(logs_dir))
        except OSError:
            return results

        for entry in entries:
            # Only consider files whose names end with _session.log.
            if not entry.endswith("_session.log"):
                continue

            file_date, file_issue = _parse_filename(entry)

            # Skip files that don't parse as a known log filename format.
            if file_date is None:
                continue

            # Apply date filter.
            if since_date is not None and file_date < since_date:
                continue

            # Apply issue number filter.
            if issue_filter is not None:
                if file_issue != issue_filter:
                    continue

            filepath = os.path.join(logs_dir, entry)
            if not os.path.isfile(filepath):
                continue

            parsed = self.parse_file(filepath)
            results.append(parsed)

        return results

    # ------------------------------------------------------------------
    # Private helpers
    # ------------------------------------------------------------------

    @staticmethod
    def _extract_context(raw_lines: list, center_idx: int, window: int = 2) -> list:
        """
        Return up to *window* parsed lines before and after *center_idx*.

        Only lines that match the base log format are included.  The line at
        *center_idx* itself is NOT included (it is already captured in the
        error entry's ``message`` field).

        Parameters
        ----------
        raw_lines : list[str]
            All raw lines from the log file.
        center_idx : int
            Index of the error line within *raw_lines*.
        window : int
            Number of surrounding lines to capture on each side (default 2).

        Returns
        -------
        list[dict]
            Parsed line dicts for the surrounding context.
        """
        start = max(0, center_idx - window)
        end = min(len(raw_lines), center_idx + window + 1)

        context: list = []
        for i in range(start, end):
            if i == center_idx:
                continue
            parsed = _parse_line(raw_lines[i])
            if parsed is not None:
                context.append(
                    {
                        "timestamp": parsed["timestamp"],
                        "level": parsed["level"],
                        "stage": parsed["stage"],
                        "message": parsed["message"],
                    }
                )
        return context
