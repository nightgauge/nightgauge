"""
history_parser.py — JSONL execution history parser for the retro engine.

Parses daily JSONL files written by the TypeScript ExecutionHistoryWriter:
  .nightgauge/pipeline/history/YYYY-MM-DD.jsonl

Each line is one JSON object conforming to the ExecutionHistoryRunRecordV2 or
ExecutionOutcomeRecordV2 schema (schema_version "1" records are also accepted).

Canonical schema source:
  packages/nightgauge-vscode/src/utils/executionHistoryWriter.ts
  packages/nightgauge-vscode/src/schemas/executionHistory.ts
"""

import json
import os
import re
import warnings
from typing import Any

# Statuses that constitute a stage-level failure.
_FAILURE_STATUSES = frozenset(("failed", "error", "timeout", "cancelled"))

# Pattern for valid history filenames: YYYY-MM-DD.jsonl
_FILENAME_PATTERN = re.compile(r"^\d{4}-\d{2}-\d{2}\.jsonl$")


class HistoryParser:
    """Parse JSONL execution history files written by ExecutionHistoryWriter.

    All methods are stateless; no instance state is maintained between calls.
    """

    # ------------------------------------------------------------------
    # Public API
    # ------------------------------------------------------------------

    def parse_file(self, filepath: str) -> dict:
        """Parse a single JSONL history file.

        Parameters
        ----------
        filepath:
            Absolute or relative path to a YYYY-MM-DD.jsonl file.

        Returns
        -------
        dict with keys:
            date (str | None)
                Date string extracted from the filename (YYYY-MM-DD), or None
                when the filename does not match the expected pattern.
            records (list[dict])
                Successfully parsed JSON objects, one per non-empty line.
            skipped (int)
                Number of lines that could not be parsed as valid JSON.
        """
        date = self._date_from_path(filepath)
        records: list[dict] = []
        skipped = 0

        try:
            with open(filepath, encoding="utf-8") as fh:
                for lineno, raw_line in enumerate(fh, start=1):
                    stripped = raw_line.strip()
                    if not stripped:
                        # Empty lines are silently ignored.
                        continue
                    try:
                        record = json.loads(stripped)
                        if isinstance(record, dict):
                            records.append(record)
                        else:
                            # Valid JSON but not an object — treat as malformed.
                            warnings.warn(
                                f"{filepath}:{lineno}: expected JSON object, "
                                f"got {type(record).__name__} — skipping",
                                stacklevel=2,
                            )
                            skipped += 1
                    except json.JSONDecodeError as exc:
                        warnings.warn(
                            f"{filepath}:{lineno}: malformed JSON ({exc}) — skipping",
                            stacklevel=2,
                        )
                        skipped += 1
        except FileNotFoundError:
            warnings.warn(f"history file not found: {filepath}", stacklevel=2)
        except OSError as exc:
            warnings.warn(
                f"could not read history file {filepath}: {exc}", stacklevel=2
            )

        return {"date": date, "records": records, "skipped": skipped}

    def parse_directory(
        self,
        history_dir: str,
        since_date: str | None = None,
        issue_filter: int | None = None,
        all_failures: bool = False,
    ) -> dict:
        """Parse all JSONL history files in a directory.

        Files whose names do not match YYYY-MM-DD.jsonl are silently skipped.

        Parameters
        ----------
        history_dir:
            Path to the directory containing YYYY-MM-DD.jsonl files
            (e.g. ``.nightgauge/pipeline/history``).
        since_date:
            ISO date string ``YYYY-MM-DD``.  Files dated before this value are
            skipped unless ``all_failures`` is True.
        issue_filter:
            When provided, only records whose ``issue_number`` matches this
            value are included in all output lists.
        all_failures:
            When True, the ``since_date`` filter is ignored when collecting
            failures — every file is scanned regardless of date.

        Returns
        -------
        dict with keys:
            runs (list[dict])
                Records with ``record_type == "run"``.
            outcomes (list[dict])
                Records with ``record_type == "outcome"``.
            failures (list[dict])
                Run records where ``outcome != "complete"`` OR any stage
                status is in (``failed``, ``error``, ``timeout``,
                ``cancelled``).
            skipped_records (int)
                Total count of malformed lines across all files.
        """
        runs: list[dict] = []
        outcomes: list[dict] = []
        failures: list[dict] = []
        skipped_records = 0

        try:
            filenames = sorted(os.listdir(history_dir))
        except FileNotFoundError:
            warnings.warn(f"history directory not found: {history_dir}", stacklevel=2)
            return {
                "runs": runs,
                "outcomes": outcomes,
                "failures": failures,
                "skipped_records": skipped_records,
            }
        except OSError as exc:
            warnings.warn(
                f"could not list history directory {history_dir}: {exc}",
                stacklevel=2,
            )
            return {
                "runs": runs,
                "outcomes": outcomes,
                "failures": failures,
                "skipped_records": skipped_records,
            }

        for filename in filenames:
            if not _FILENAME_PATTERN.match(filename):
                continue

            file_date = filename[: len("YYYY-MM-DD")]  # strip .jsonl suffix

            # Determine whether to include this file for runs/outcomes and/or
            # for failure scanning separately.
            include_in_main = since_date is None or file_date >= since_date
            include_in_failures = all_failures or include_in_main

            if not include_in_main and not include_in_failures:
                continue

            filepath = os.path.join(history_dir, filename)
            parsed = self.parse_file(filepath)
            skipped_records += parsed["skipped"]

            for record in parsed["records"]:
                record_type = record.get("record_type", "")
                issue_number = record.get("issue_number")

                # Apply issue filter.
                if issue_filter is not None and issue_number != issue_filter:
                    continue

                if record_type == "run":
                    if include_in_main:
                        runs.append(record)

                    if include_in_failures and self._is_failure(record):
                        failures.append(record)

                elif record_type == "outcome":
                    if include_in_main:
                        outcomes.append(record)

        return {
            "runs": runs,
            "outcomes": outcomes,
            "failures": failures,
            "skipped_records": skipped_records,
        }

    # ------------------------------------------------------------------
    # Private helpers
    # ------------------------------------------------------------------

    @staticmethod
    def _date_from_path(filepath: str) -> str | None:
        """Return the YYYY-MM-DD date string from the file basename, or None."""
        basename = os.path.basename(filepath)
        if _FILENAME_PATTERN.match(basename):
            return basename[: len("YYYY-MM-DD")]  # "2026-02-21.jsonl" → "2026-02-21"
        return None

    @staticmethod
    def _is_failure(run: dict) -> bool:
        """Return True when a run record represents a failed execution.

        A run is considered a failure when:
        - ``outcome`` is not ``"complete"``  (e.g. ``"failed"`` or ``"cancelled"``), OR
        - any stage has a status in the ``_FAILURE_STATUSES`` set.
        """
        outcome = run.get("outcome", "")
        if outcome != "complete":
            return True

        stages: Any = run.get("stages", {})
        if isinstance(stages, dict):
            for stage_data in stages.values():
                if (
                    isinstance(stage_data, dict)
                    and stage_data.get("status") in _FAILURE_STATUSES
                ):
                    return True

        return False
