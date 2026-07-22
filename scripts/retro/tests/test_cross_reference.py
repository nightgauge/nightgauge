"""
Unit tests for scripts/retro/cross_reference.py.

Covers:
  - routing comparison (match and mismatch)
  - budget comparison (within budget and exceeded)
  - size resolution (direct field, labels, default)
  - cross_reference_all batch method
  - unknown stage fallback
  - load_complexity_model with missing workspace
"""

from __future__ import annotations

import sys
import os

# Ensure the retro scripts directory is importable
sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from cross_reference import (
    CrossReferenceEngine,
    DEFAULT_BUDGETS,
    MODEL_MATRIX,
    STAGE_CATEGORIES,
)


# ---------------------------------------------------------------------------
# Helper
# ---------------------------------------------------------------------------


def make_classified_event(
    issue=1,
    stage="feature-dev",
    size=None,
    labels=None,
    cost=5.0,
    stages=None,
):
    return {
        "issue_number": issue,
        "title": f"Test #{issue}",
        "failure_stage": stage,
        "failure_category": "budget-exceeded",
        "confidence": "high",
        "root_cause_summary": "test",
        "evidence": [],
        "failed_stages": [stage],
        "estimated_cost_usd": cost,
        "total_duration_ms": 600000,
        "size": size,
        "labels": labels or [],
        "stages": stages or {},
    }


# ---------------------------------------------------------------------------
# Canonical budget value sanity checks
# (Guards against accidental drift from budgetEnforcer.ts)
# ---------------------------------------------------------------------------


def test_canonical_budget_feature_dev_m():
    assert DEFAULT_BUDGETS["feature-dev"]["M"] == 8.0


def test_canonical_budget_feature_dev_l():
    assert DEFAULT_BUDGETS["feature-dev"]["L"] == 25.0


def test_canonical_budget_feature_validate_s():
    assert DEFAULT_BUDGETS["feature-validate"]["S"] == 2.0


# ---------------------------------------------------------------------------
# 1. test_routing_comparison_match
# ---------------------------------------------------------------------------


def test_routing_comparison_match():
    """failure_stage='feature-dev', size='L' → expected_model='opus'."""
    engine = CrossReferenceEngine()
    event = make_classified_event(stage="feature-dev", size="L", cost=10.0)
    result = engine.cross_reference(event)

    rc = result["routing_comparison"]
    assert rc["expected_model"] == "opus", (
        f"Expected 'opus' for feature-dev/L but got {rc['expected_model']!r}"
    )
    assert rc["expected_size"] == "L"
    assert rc["actual_size"] == "L"
    # actual_model is 'unknown' when no stages data is present — no mismatch
    assert rc["routing_mismatch"] is False


# ---------------------------------------------------------------------------
# 2. test_routing_comparison_mismatch
# ---------------------------------------------------------------------------


def test_routing_comparison_mismatch():
    """Actual model 'sonnet' but expected 'opus' → routing_mismatch=True."""
    engine = CrossReferenceEngine()
    event = make_classified_event(
        stage="feature-dev",
        size="L",
        cost=10.0,
        stages={
            "feature-dev": {"model_selection": "sonnet"},
        },
    )
    result = engine.cross_reference(event)

    rc = result["routing_comparison"]
    assert rc["expected_model"] == "opus"
    assert rc["actual_model"] == "sonnet"
    assert rc["routing_mismatch"] is True


# ---------------------------------------------------------------------------
# 3. test_budget_comparison_within_budget
# ---------------------------------------------------------------------------


def test_budget_comparison_within_budget():
    """cost=$5.0 for feature-dev size M (budget $8.0) → exceeded=False, utilization ~62.5%."""
    engine = CrossReferenceEngine()
    event = make_classified_event(stage="feature-dev", size="M", cost=5.0)
    result = engine.cross_reference(event)

    bc = result["budget_comparison"]
    assert bc["expected_budget_usd"] == 8.0
    assert bc["actual_cost_usd"] == 5.0
    assert bc["exceeded"] is False
    assert abs(bc["budget_utilization_pct"] - 62.5) < 0.01, (
        f"Expected utilization ~62.5% but got {bc['budget_utilization_pct']}"
    )


# ---------------------------------------------------------------------------
# 4. test_budget_comparison_exceeded
# ---------------------------------------------------------------------------


def test_budget_comparison_exceeded():
    """cost=$30.0 for feature-dev size L (budget $25.0) → exceeded=True."""
    engine = CrossReferenceEngine()
    event = make_classified_event(stage="feature-dev", size="L", cost=30.0)
    result = engine.cross_reference(event)

    bc = result["budget_comparison"]
    assert bc["expected_budget_usd"] == 25.0
    assert bc["actual_cost_usd"] == 30.0
    assert bc["exceeded"] is True
    expected_utilization = round((30.0 / 25.0) * 100.0, 2)
    assert abs(bc["budget_utilization_pct"] - expected_utilization) < 0.01


# ---------------------------------------------------------------------------
# 5. test_size_resolution_direct
# ---------------------------------------------------------------------------


def test_size_resolution_direct():
    """Event with size='L' resolves to 'L'."""
    engine = CrossReferenceEngine()
    event = make_classified_event(size="L")
    resolved = engine._resolve_size(event)
    assert resolved == "L"


# ---------------------------------------------------------------------------
# 6. test_size_resolution_from_labels
# ---------------------------------------------------------------------------


def test_size_resolution_from_labels():
    """Event with labels=['size:M'] and no direct size field resolves to 'M'."""
    engine = CrossReferenceEngine()
    event = make_classified_event(size=None, labels=["size:M"])
    resolved = engine._resolve_size(event)
    assert resolved == "M"


# ---------------------------------------------------------------------------
# 7. test_size_resolution_default
# ---------------------------------------------------------------------------


def test_size_resolution_default():
    """Event with no size info defaults to 'M'."""
    engine = CrossReferenceEngine()
    event = make_classified_event(size=None, labels=[])
    resolved = engine._resolve_size(event)
    assert resolved == "M"


# ---------------------------------------------------------------------------
# 8. test_cross_reference_all
# ---------------------------------------------------------------------------


def test_cross_reference_all():
    """cross_reference_all returns one enriched dict per input event."""
    engine = CrossReferenceEngine()
    events = [
        make_classified_event(issue=1, stage="feature-dev", size="M", cost=5.0),
        make_classified_event(issue=2, stage="feature-validate", size="S", cost=1.5),
        make_classified_event(issue=3, stage="pr-merge", size="L", cost=0.8),
    ]
    results = engine.cross_reference_all(events)

    assert len(results) == 3

    for original, enriched in zip(events, results):
        assert "routing_comparison" in enriched
        assert "budget_comparison" in enriched
        # Original dict must not be mutated
        assert "routing_comparison" not in original

    # Spot-check first result
    assert results[0]["issue_number"] == 1
    assert results[0]["budget_comparison"]["expected_budget_usd"] == 8.0

    # Spot-check second result (feature-validate S → budget $2.0)
    assert results[1]["budget_comparison"]["expected_budget_usd"] == 2.0
    assert results[1]["budget_comparison"]["exceeded"] is False


# ---------------------------------------------------------------------------
# 9. test_unknown_stage
# ---------------------------------------------------------------------------


def test_unknown_stage():
    """Event with failure_stage='custom-stage' falls back to 'dev' category."""
    engine = CrossReferenceEngine()
    event = make_classified_event(stage="custom-stage", size="L", cost=10.0)
    result = engine.cross_reference(event)

    rc = result["routing_comparison"]
    # 'custom-stage' is not in STAGE_CATEGORIES → defaults to "dev" category
    # MODEL_MATRIX["dev"]["L"] == "opus"
    assert rc["expected_model"] == MODEL_MATRIX["dev"]["L"]

    bc = result["budget_comparison"]
    # 'custom-stage' is not in DEFAULT_BUDGETS → budget 0.0, utilization 0.0
    assert bc["expected_budget_usd"] == 0.0
    assert bc["budget_utilization_pct"] == 0.0
    assert bc["exceeded"] is False


# ---------------------------------------------------------------------------
# 10. test_load_complexity_model_missing
# ---------------------------------------------------------------------------


def test_load_complexity_model_missing(tmp_path):
    """Non-existent workspace returns None from load_complexity_model."""
    non_existent = str(tmp_path / "no_such_workspace")
    engine = CrossReferenceEngine(workspace_root=non_existent)
    result = engine.load_complexity_model()
    assert result is None


# ---------------------------------------------------------------------------
# Additional edge-case coverage
# ---------------------------------------------------------------------------


def test_routing_no_mismatch_when_actual_unknown():
    """routing_mismatch is False when actual_model cannot be determined."""
    engine = CrossReferenceEngine()
    # No stages data → actual_model == "unknown"
    event = make_classified_event(stage="feature-dev", size="L", cost=10.0)
    result = engine.cross_reference(event)

    rc = result["routing_comparison"]
    assert rc["actual_model"] == "unknown"
    assert rc["routing_mismatch"] is False


def test_size_label_case_insensitive():
    """Labels like 'size:xl' (lowercase) are normalised correctly."""
    engine = CrossReferenceEngine()
    event = make_classified_event(size=None, labels=["size:xl"])
    resolved = engine._resolve_size(event)
    assert resolved == "XL"


def test_cross_reference_does_not_mutate_original():
    """cross_reference must return a new dict without mutating the input."""
    engine = CrossReferenceEngine()
    original = make_classified_event(stage="feature-dev", size="M", cost=3.0)
    original_keys = set(original.keys())

    _ = engine.cross_reference(original)

    assert set(original.keys()) == original_keys
    assert "routing_comparison" not in original
    assert "budget_comparison" not in original


def test_actual_model_extracted_from_stage_data():
    """actual_model is taken from stages[failure_stage]['model_selection']."""
    engine = CrossReferenceEngine()
    event = make_classified_event(
        stage="feature-dev",
        size="M",
        cost=3.0,
        stages={"feature-dev": {"model_selection": "Opus"}},
    )
    result = engine.cross_reference(event)

    rc = result["routing_comparison"]
    # Value should be lower-cased
    assert rc["actual_model"] == "opus"


def test_budget_zero_cost():
    """Zero-cost event yields 0% utilization and not exceeded."""
    engine = CrossReferenceEngine()
    event = make_classified_event(stage="feature-dev", size="M", cost=0.0)
    result = engine.cross_reference(event)

    bc = result["budget_comparison"]
    assert bc["actual_cost_usd"] == 0.0
    assert bc["budget_utilization_pct"] == 0.0
    assert bc["exceeded"] is False
