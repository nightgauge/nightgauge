"""
Cross-reference engine for the nightgauge-retro pipeline analysis skill.

Cross-references classified failure events against:
  1. Budget enforcer defaults — hardcoded mirror from:
       packages/nightgauge-vscode/src/utils/budgetEnforcer.ts
       (DEFAULT_SIZE_AWARE_BUDGETS)
  2. Model routing matrix — hardcoded mirror from:
       packages/nightgauge-sdk/src/analysis/AutoModelSelector.ts
       (STAGE_COMPLEXITY_MATRIX / STAGE_CATEGORIES)
  3. Complexity model file — optional enrichment from:
       .nightgauge/complexity-model.yaml (workspace-local, optional)

No required external dependencies. PyYAML is used when available for the
complexity model loader; falls back to a minimal line-by-line parser or skips
the file entirely if it does not exist. Requires Python 3.8+.
"""

from __future__ import annotations

import os
import re
from typing import Any


# ---------------------------------------------------------------------------
# Budget defaults
# Mirror of DEFAULT_SIZE_AWARE_BUDGETS in budgetEnforcer.ts
# Source: packages/nightgauge-vscode/src/utils/budgetEnforcer.ts
# ---------------------------------------------------------------------------

DEFAULT_BUDGETS: dict[str, dict[str, float]] = {
    "issue-pickup": {"XS": 0.15, "S": 0.15, "M": 0.75, "L": 0.75, "XL": 1.0},
    "feature-planning": {"XS": 1.5, "S": 2.0, "M": 2.5, "L": 3.5, "XL": 5.0},
    "feature-dev": {"XS": 2.0, "S": 4.0, "M": 8.0, "L": 25.0, "XL": 50.0},
    "feature-validate": {"XS": 1.0, "S": 2.0, "M": 10.0, "L": 20.0, "XL": 35.0},
    "pr-create": {"XS": 0.1, "S": 0.1, "M": 0.15, "L": 0.2, "XL": 0.5},
    "pr-merge": {"XS": 0.4, "S": 0.4, "M": 0.8, "L": 1.5, "XL": 3.0},
}

# ---------------------------------------------------------------------------
# Model routing matrix
# Mirror of STAGE_COMPLEXITY_MATRIX in AutoModelSelector.ts
# Source: packages/nightgauge-sdk/src/analysis/AutoModelSelector.ts
# ---------------------------------------------------------------------------

MODEL_MATRIX: dict[str, dict[str, str]] = {
    "planning": {
        "XS": "sonnet",
        "S": "sonnet",
        "M": "sonnet",
        "L": "opus",
        "XL": "opus",
    },
    "dev": {"XS": "sonnet", "S": "sonnet", "M": "sonnet", "L": "opus", "XL": "opus"},
    "validate": {"XS": "haiku", "S": "haiku", "M": "sonnet", "L": "opus", "XL": "opus"},
    "lightweight": {
        "XS": "haiku",
        "S": "haiku",
        "M": "haiku",
        "L": "haiku",
        "XL": "haiku",
    },
    "merge": {"XS": "haiku", "S": "haiku", "M": "haiku", "L": "sonnet", "XL": "sonnet"},
}

# ---------------------------------------------------------------------------
# Stage → category mapping
# Mirrors categorizeStage() in AutoModelSelector.ts
# ---------------------------------------------------------------------------

STAGE_CATEGORIES: dict[str, str] = {
    "issue-pickup": "lightweight",
    "feature-planning": "planning",
    "feature-dev": "dev",
    "feature-validate": "validate",
    "pr-create": "lightweight",
    "pr-merge": "merge",
}

# Valid size labels, in ascending complexity order
_VALID_SIZES = ("XS", "S", "M", "L", "XL")

# Regex to extract a size label from a GitHub-style "size:X" label string
_SIZE_LABEL_RE = re.compile(r"^size:([A-Za-z]+)$", re.IGNORECASE)


class CrossReferenceEngine:
    """
    Cross-references classified failure events against canonical budget and
    model-routing data hardcoded from the TypeScript sources.

    Optionally enriches events with data from the workspace complexity model
    file (.nightgauge/complexity-model.yaml) when present.

    Usage::

        engine = CrossReferenceEngine(workspace_root="/path/to/repo")
        enriched = engine.cross_reference(classified_event)
        all_enriched = engine.cross_reference_all(classified_events)
    """

    def __init__(self, workspace_root: str = ".") -> None:
        """
        Parameters
        ----------
        workspace_root:
            Absolute (or relative) path to the repository root.  Used only
            for locating .nightgauge/complexity-model.yaml.
        """
        self._workspace_root = workspace_root
        self._complexity_model: dict | None = _UNSET  # type: ignore[assignment]

    # ------------------------------------------------------------------
    # Public API
    # ------------------------------------------------------------------

    def cross_reference(self, classified_event: dict) -> dict:
        """Cross-reference a single classified failure event.

        Adds ``routing_comparison`` and ``budget_comparison`` keys to a copy
        of the event dict. The original dict is not modified.

        Parameters
        ----------
        classified_event:
            A classified failure event dict as produced by
            ``FailureClassifier.classify()``. Expected keys include:
            ``failure_stage``, ``size``, ``labels``, ``estimated_cost_usd``,
            and optionally ``stages`` (for actual model extraction).

        Returns
        -------
        dict
            A shallow copy of the event with two additional keys:

            ``routing_comparison``::

                {
                    "expected_model": "opus",   # from MODEL_MATRIX
                    "actual_model":   "opus",   # from event or "unknown"
                    "expected_size":  "L",      # size resolved from event
                    "actual_size":    "L",      # same as expected_size
                    "routing_mismatch": False   # True when expected != actual
                }

            ``budget_comparison``::

                {
                    "expected_budget_usd":   25.0,  # DEFAULT_BUDGETS[stage][size]
                    "actual_cost_usd":       12.5,  # from estimated_cost_usd
                    "budget_utilization_pct": 50.0, # (actual / expected) * 100
                    "exceeded": False               # True when actual > expected
                }
        """
        event = dict(classified_event)

        failure_stage = event.get("failure_stage") or "unknown"
        size = self._resolve_size(event)

        routing_comparison = self._build_routing_comparison(
            event=event,
            failure_stage=failure_stage,
            size=size,
        )

        budget_comparison = self._build_budget_comparison(
            event=event,
            failure_stage=failure_stage,
            size=size,
        )

        event["routing_comparison"] = routing_comparison
        event["budget_comparison"] = budget_comparison

        return event

    def cross_reference_all(self, classified_events: list[dict]) -> list[dict]:
        """Cross-reference a list of classified failure events.

        Parameters
        ----------
        classified_events:
            List of classified failure event dicts.

        Returns
        -------
        list[dict]
            List of enriched dicts in the same order.
        """
        return [self.cross_reference(event) for event in classified_events]

    def load_complexity_model(self) -> dict | None:
        """Load and parse .nightgauge/complexity-model.yaml if it exists.

        Attempts PyYAML first. If PyYAML is not installed, falls back to a
        minimal line-by-line parser that handles simple ``key: value`` pairs
        at the top level only. Returns ``None`` when the file does not exist
        or cannot be parsed.

        Returns
        -------
        dict or None
            Parsed YAML document as a dict, or None on failure / missing file.
        """
        model_path = os.path.join(
            self._workspace_root,
            ".nightgauge",
            "complexity-model.yaml",
        )

        if not os.path.isfile(model_path):
            return None

        try:
            with open(model_path, "r", encoding="utf-8") as fh:
                content = fh.read()
        except OSError:
            return None

        # Try PyYAML first
        try:
            import yaml  # type: ignore[import]

            parsed = yaml.safe_load(content)
            if isinstance(parsed, dict):
                return parsed
            return None
        except ImportError:
            pass
        except Exception:
            return None

        # Minimal fallback: parse top-level "key: value" lines only
        return _parse_simple_yaml(content)

    # ------------------------------------------------------------------
    # Internal helpers
    # ------------------------------------------------------------------

    def _resolve_size(self, event: dict) -> str:
        """Resolve the size label for an event.

        Resolution order:
        1. ``event["size"]`` — direct size field (already a label like "M")
        2. ``event["labels"]`` — list of GitHub label strings; looks for
           the first ``"size:X"`` entry
        3. Default: ``"M"``

        Always returns an upper-case string from ``_VALID_SIZES``.
        """
        # Direct size field
        direct = event.get("size")
        if isinstance(direct, str):
            candidate = direct.strip().upper()
            if candidate in _VALID_SIZES:
                return candidate

        # Search label list
        labels = event.get("labels")
        if isinstance(labels, list):
            for label in labels:
                if not isinstance(label, str):
                    continue
                m = _SIZE_LABEL_RE.match(label.strip())
                if m:
                    candidate = m.group(1).upper()
                    if candidate in _VALID_SIZES:
                        return candidate

        return "M"

    @staticmethod
    def _resolve_actual_model(event: dict) -> str:
        """Extract the actual model used from the event's stage data.

        Looks for model selection information in the ``stages`` dict that
        some parsers attach to events.  Returns ``"unknown"`` when no data
        is found.

        The following locations are checked in order:
        1. ``event["stages"][<failure_stage>]["model_selection"]``
        2. ``event["stages"][<failure_stage>]["model"]``
        3. Any stage in ``event["stages"]`` that contains model information
           (fallback scan)
        4. ``event["model_selection"]`` at the top level
        5. ``event["model"]`` at the top level
        """
        stages: dict[str, Any] = event.get("stages") or {}
        failure_stage: str = event.get("failure_stage") or ""

        # Check the specific failed stage first
        if failure_stage and failure_stage in stages:
            stage_data = stages[failure_stage]
            if isinstance(stage_data, dict):
                model = stage_data.get("model_selection") or stage_data.get("model")
                if isinstance(model, str) and model:
                    return model.lower()

        # Fallback: scan all stages for model information
        for stage_data in stages.values():
            if isinstance(stage_data, dict):
                model = stage_data.get("model_selection") or stage_data.get("model")
                if isinstance(model, str) and model:
                    return model.lower()

        # Top-level fallback
        model = event.get("model_selection") or event.get("model")
        if isinstance(model, str) and model:
            return model.lower()

        return "unknown"

    @staticmethod
    def _build_routing_comparison(
        event: dict,
        failure_stage: str,
        size: str,
    ) -> dict:
        """Build the routing_comparison dict for an event.

        Parameters
        ----------
        event:
            The classified event dict.
        failure_stage:
            The resolved failure stage string.
        size:
            The resolved size label (e.g. "M").

        Returns
        -------
        dict
            routing_comparison with keys: expected_model, actual_model,
            expected_size, actual_size, routing_mismatch.
        """
        stage_category = STAGE_CATEGORIES.get(failure_stage, "dev")
        size_matrix = MODEL_MATRIX.get(stage_category, {})
        expected_model: str = size_matrix.get(size, "unknown")

        actual_model = CrossReferenceEngine._resolve_actual_model(event)

        routing_mismatch = (
            expected_model != "unknown"
            and actual_model != "unknown"
            and expected_model != actual_model
        )

        return {
            "expected_model": expected_model,
            "actual_model": actual_model,
            "expected_size": size,
            "actual_size": size,
            "routing_mismatch": routing_mismatch,
        }

    @staticmethod
    def _build_budget_comparison(
        event: dict,
        failure_stage: str,
        size: str,
    ) -> dict:
        """Build the budget_comparison dict for an event.

        Parameters
        ----------
        event:
            The classified event dict.
        failure_stage:
            The resolved failure stage string.
        size:
            The resolved size label (e.g. "M").

        Returns
        -------
        dict
            budget_comparison with keys: expected_budget_usd,
            actual_cost_usd, budget_utilization_pct, exceeded.
        """
        stage_budgets = DEFAULT_BUDGETS.get(failure_stage, {})
        expected_budget: float = stage_budgets.get(size, 0.0)

        actual_cost_raw = event.get("estimated_cost_usd")
        if isinstance(actual_cost_raw, (int, float)) and actual_cost_raw >= 0:
            actual_cost = float(actual_cost_raw)
        else:
            actual_cost = 0.0

        if expected_budget > 0:
            utilization_pct = round((actual_cost / expected_budget) * 100.0, 2)
        else:
            utilization_pct = 0.0

        exceeded = expected_budget > 0 and actual_cost > expected_budget

        return {
            "expected_budget_usd": expected_budget,
            "actual_cost_usd": actual_cost,
            "budget_utilization_pct": utilization_pct,
            "exceeded": exceeded,
        }


# ---------------------------------------------------------------------------
# Minimal YAML fallback parser
# ---------------------------------------------------------------------------


def _parse_simple_yaml(content: str) -> dict | None:
    """Parse a very small subset of YAML: top-level ``key: value`` lines only.

    Handles string values, integers, floats, and bare booleans. Ignores
    comments, blank lines, and any nested/indented blocks.  This is an
    intentionally limited fallback used only when PyYAML is unavailable.

    Returns a dict of parsed key→value pairs, or None on complete failure.
    """
    result: dict[str, Any] = {}
    # Matches: optional-spaces, key (no colon), colon, optional-space, value
    line_re = re.compile(r"^([A-Za-z_][\w-]*)\s*:\s*(.*)")

    for raw_line in content.splitlines():
        line = raw_line.strip()
        if not line or line.startswith("#"):
            continue
        # Skip indented lines (nested structures)
        if raw_line and raw_line[0] in (" ", "\t"):
            continue

        m = line_re.match(line)
        if not m:
            continue

        key = m.group(1)
        raw_val = m.group(2).strip()

        # Strip inline comments
        if " #" in raw_val:
            raw_val = raw_val[: raw_val.index(" #")].rstrip()

        # Strip surrounding quotes
        if (raw_val.startswith('"') and raw_val.endswith('"')) or (
            raw_val.startswith("'") and raw_val.endswith("'")
        ):
            result[key] = raw_val[1:-1]
            continue

        # Boolean literals
        if raw_val.lower() == "true":
            result[key] = True
            continue
        if raw_val.lower() == "false":
            result[key] = False
            continue
        if raw_val.lower() in ("null", "~", ""):
            result[key] = None
            continue

        # Numeric
        try:
            result[key] = int(raw_val)
            continue
        except ValueError:
            pass
        try:
            result[key] = float(raw_val)
            continue
        except ValueError:
            pass

        # Plain string
        result[key] = raw_val

    return result if result else None


# Sentinel used to detect uninitialized cache vs. loaded-but-None
class _UnsetType:
    _instance: _UnsetType | None = None

    def __new__(cls) -> _UnsetType:
        if cls._instance is None:
            cls._instance = super().__new__(cls)
        return cls._instance


_UNSET = _UnsetType()
