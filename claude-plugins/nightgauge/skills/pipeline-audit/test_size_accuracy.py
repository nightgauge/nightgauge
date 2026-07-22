"""
Tests for size estimate accuracy calculation logic (Issue #1591).

Validates the accuracy computation extracted from the pipeline-audit
SKILL.md Python script. Run with: python3 -m pytest test_size_accuracy.py -v
Or standalone: python3 test_size_accuracy.py
"""

import statistics
from collections import defaultdict
from datetime import datetime, timezone

# ---------------------------------------------------------------------------
# Production logic extracted from SKILL.md Phase 2 extraction script
# ---------------------------------------------------------------------------

SIZE_ORDER = {"XS": 0, "S": 1, "M": 2, "L": 3, "XL": 4}


def compute_size_baselines(run_metrics):
    """Compute median/avg cost and duration per size bracket."""
    size_costs = defaultdict(list)
    size_durations = defaultdict(list)
    for rm in run_metrics:
        if rm.get("size") and rm.get("estimated_cost_usd", 0) > 0:
            size_costs[rm["size"]].append(rm["estimated_cost_usd"])
        if rm.get("size") and rm.get("total_duration_ms", 0) > 0:
            size_durations[rm["size"]].append(rm["total_duration_ms"])

    baselines = {}
    for size in ("XS", "S", "M", "L", "XL"):
        costs = size_costs.get(size, [])
        durs = size_durations.get(size, [])
        if costs:
            baselines[size] = {
                "count": len(costs),
                "median_cost": round(statistics.median(costs), 4),
                "avg_cost": round(statistics.mean(costs), 4),
                "min_cost": round(min(costs), 4),
                "max_cost": round(max(costs), 4),
                "median_duration_ms": round(statistics.median(durs), 0) if durs else 0,
                "avg_duration_ms": round(statistics.mean(durs), 0) if durs else 0,
            }
    return baselines, size_costs


def compute_accuracy_rates(size_costs, baselines):
    """Compute per-size accuracy rate (% within 0.5x-2x of median)."""
    rates = {}
    for size, costs in size_costs.items():
        if size not in baselines:
            continue
        median = baselines[size]["median_cost"]
        within_range = sum(1 for c in costs if median * 0.5 <= c <= median * 2.0)
        rates[size] = {
            "total": len(costs),
            "within_range": within_range,
            "accuracy_pct": round(within_range / len(costs) * 100, 1),
        }
    return rates


def detect_mismatches(run_metrics, baselines):
    """Detect oversized and undersized issues."""
    oversized = []
    undersized = []
    for rm in run_metrics:
        sz = rm.get("size")
        cost = rm.get("estimated_cost_usd", 0)
        if not sz or sz not in SIZE_ORDER or cost <= 0:
            continue
        actual_bracket = None
        for candidate in ("XS", "S", "M", "L", "XL"):
            if candidate in baselines:
                bl = baselines[candidate]
                if bl["median_cost"] * 0.5 <= cost <= bl["median_cost"] * 2.0:
                    actual_bracket = candidate
                    break
        if actual_bracket and SIZE_ORDER.get(actual_bracket, -1) < SIZE_ORDER[sz]:
            oversized.append(
                {
                    "issue_number": rm.get("issue_number"),
                    "labeled_size": sz,
                    "actual_bracket": actual_bracket,
                    "cost_usd": round(cost, 2),
                }
            )
        elif actual_bracket and SIZE_ORDER.get(actual_bracket, -1) > SIZE_ORDER[sz]:
            undersized.append(
                {
                    "issue_number": rm.get("issue_number"),
                    "labeled_size": sz,
                    "actual_bracket": actual_bracket,
                    "cost_usd": round(cost, 2),
                }
            )
    return oversized, undersized


def compute_weekly_trend(run_metrics, baselines):
    """Compute weekly sizing accuracy trend."""
    weekly = defaultdict(lambda: {"total": 0, "accurate": 0})
    for rm in run_metrics:
        sz = rm.get("size")
        cost = rm.get("estimated_cost_usd", 0)
        started = rm.get("started_at", "")
        if not sz or sz not in baselines or cost <= 0 or not started:
            continue
        try:
            dt = datetime.fromisoformat(started.replace("Z", "+00:00"))
            week_key = f"{dt.isocalendar()[0]}-W{dt.isocalendar()[1]:02d}"
        except (ValueError, AttributeError):
            continue
        median = baselines[sz]["median_cost"]
        weekly[week_key]["total"] += 1
        if median * 0.5 <= cost <= median * 2.0:
            weekly[week_key]["accurate"] += 1

    result = []
    for wk in sorted(weekly.keys()):
        d = weekly[wk]
        result.append(
            {
                "week": wk,
                "total": d["total"],
                "accurate": d["accurate"],
                "accuracy_pct": round(d["accurate"] / d["total"] * 100, 1)
                if d["total"]
                else 0,
            }
        )
    return result


# ---------------------------------------------------------------------------
# Test helpers
# ---------------------------------------------------------------------------


def make_run(issue, size, cost, duration_ms=600000, started_at="2026-02-15T10:00:00Z"):
    return {
        "issue_number": issue,
        "title": f"Issue #{issue}",
        "size": size,
        "estimated_cost_usd": cost,
        "total_duration_ms": duration_ms,
        "started_at": started_at,
    }


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------


def test_baselines_median_and_avg():
    """Baselines compute correct median and average per size."""
    runs = [
        make_run(1, "S", 2.0),
        make_run(2, "S", 4.0),
        make_run(3, "S", 6.0),
        make_run(4, "L", 10.0),
        make_run(5, "L", 14.0),
    ]
    baselines, _ = compute_size_baselines(runs)
    assert baselines["S"]["median_cost"] == 4.0
    assert baselines["S"]["avg_cost"] == 4.0
    assert baselines["S"]["count"] == 3
    assert baselines["L"]["median_cost"] == 12.0
    assert baselines["L"]["avg_cost"] == 12.0
    assert baselines["L"]["count"] == 2


def test_baselines_skips_missing_size():
    """Runs without size field are excluded from baselines."""
    runs = [
        make_run(1, "S", 3.0),
        make_run(2, None, 5.0),
        make_run(3, "S", 5.0),
    ]
    baselines, size_costs = compute_size_baselines(runs)
    assert "S" in baselines
    assert baselines["S"]["count"] == 2
    assert None not in baselines


def test_baselines_skips_zero_cost():
    """Runs with zero cost are excluded."""
    runs = [
        make_run(1, "M", 0.0),
        make_run(2, "M", 7.0),
    ]
    baselines, _ = compute_size_baselines(runs)
    assert baselines["M"]["count"] == 1
    assert baselines["M"]["median_cost"] == 7.0


def test_accuracy_rate_all_within_range():
    """100% accuracy when all costs are within 0.5x-2x of median."""
    runs = [
        make_run(1, "S", 3.0),
        make_run(2, "S", 4.0),
        make_run(3, "S", 5.0),
    ]
    baselines, size_costs = compute_size_baselines(runs)
    rates = compute_accuracy_rates(size_costs, baselines)
    assert rates["S"]["accuracy_pct"] == 100.0
    assert rates["S"]["within_range"] == 3


def test_accuracy_rate_with_outliers():
    """Outliers outside 0.5x-2x range reduce accuracy."""
    runs = [
        make_run(1, "M", 6.0),
        make_run(2, "M", 7.0),
        make_run(3, "M", 8.0),
        make_run(4, "M", 50.0),  # far above 2x of median (7.0)
        make_run(5, "M", 1.0),  # far below 0.5x of median (7.0)
    ]
    baselines, size_costs = compute_size_baselines(runs)
    # median is 7.0; range is 3.5 to 14.0
    rates = compute_accuracy_rates(size_costs, baselines)
    assert rates["M"]["total"] == 5
    assert rates["M"]["within_range"] == 3
    assert rates["M"]["accuracy_pct"] == 60.0


def test_accuracy_rate_boundary_values():
    """Costs exactly at 0.5x and 2x boundaries are counted as within range."""
    runs = [
        make_run(1, "S", 10.0),
        make_run(2, "S", 5.0),  # exactly 0.5x of median 10.0
        make_run(3, "S", 20.0),  # exactly 2.0x of median 10.0
    ]
    baselines, size_costs = compute_size_baselines(runs)
    rates = compute_accuracy_rates(size_costs, baselines)
    assert rates["S"]["within_range"] == 3
    assert rates["S"]["accuracy_pct"] == 100.0


def test_oversize_detection():
    """Issues labeled bigger than actual cost bracket are flagged as oversized."""
    runs = [
        make_run(1, "S", 3.0),
        make_run(2, "S", 4.0),
        make_run(3, "S", 5.0),
        make_run(4, "L", 10.0),
        make_run(5, "L", 14.0),
        # Issue 6 is labeled L but its cost (3.5) falls within S bracket
        make_run(6, "L", 3.5),
    ]
    baselines, _ = compute_size_baselines(runs)
    oversized, undersized = detect_mismatches(runs, baselines)
    assert len(oversized) == 1
    assert oversized[0]["issue_number"] == 6
    assert oversized[0]["labeled_size"] == "L"
    assert oversized[0]["actual_bracket"] == "S"


def test_undersize_detection():
    """Issues labeled smaller than actual cost bracket are flagged as undersized."""
    runs = [
        make_run(1, "S", 3.0),
        make_run(2, "S", 4.0),
        make_run(3, "L", 10.0),
        make_run(4, "L", 14.0),
        # Issue 5 is labeled S but its cost (11.0) falls within L bracket
        make_run(5, "S", 11.0),
    ]
    baselines, _ = compute_size_baselines(runs)
    oversized, undersized = detect_mismatches(runs, baselines)
    assert len(undersized) == 1
    assert undersized[0]["issue_number"] == 5
    assert undersized[0]["labeled_size"] == "S"
    assert undersized[0]["actual_bracket"] == "L"


def test_no_mismatches_when_costs_match_labels():
    """No oversized/undersized when all costs match their labeled bracket."""
    runs = [
        make_run(1, "S", 3.0),
        make_run(2, "S", 4.0),
        make_run(3, "L", 12.0),
        make_run(4, "L", 14.0),
    ]
    baselines, _ = compute_size_baselines(runs)
    oversized, undersized = detect_mismatches(runs, baselines)
    assert len(oversized) == 0
    assert len(undersized) == 0


def test_mismatches_skip_missing_size():
    """Runs without size are not flagged as mismatches."""
    runs = [
        make_run(1, "S", 3.0),
        make_run(2, "S", 4.0),
        make_run(3, None, 3.0),
    ]
    baselines, _ = compute_size_baselines(runs)
    oversized, undersized = detect_mismatches(runs, baselines)
    assert len(oversized) == 0
    assert len(undersized) == 0


def test_weekly_trend_groups_by_week():
    """Weekly trend correctly groups runs by ISO week."""
    runs = [
        make_run(1, "S", 3.0, started_at="2026-02-09T10:00:00Z"),  # W07
        make_run(2, "S", 4.0, started_at="2026-02-10T10:00:00Z"),  # W07
        make_run(3, "S", 5.0, started_at="2026-02-16T10:00:00Z"),  # W08
        make_run(4, "S", 50.0, started_at="2026-02-17T10:00:00Z"),  # W08, outlier
    ]
    baselines, _ = compute_size_baselines(runs)
    trend = compute_weekly_trend(runs, baselines)
    assert len(trend) == 2
    assert trend[0]["week"] == "2026-W07"
    assert trend[0]["total"] == 2
    assert trend[1]["week"] == "2026-W08"
    assert trend[1]["total"] == 2


def test_weekly_trend_accuracy_calculation():
    """Weekly accuracy % is correct."""
    runs = [
        make_run(1, "S", 4.0, started_at="2026-02-09T10:00:00Z"),
        make_run(2, "S", 5.0, started_at="2026-02-10T10:00:00Z"),
        make_run(3, "S", 100.0, started_at="2026-02-11T10:00:00Z"),  # outlier
    ]
    baselines, _ = compute_size_baselines(runs)
    # median S = 5.0, range = 2.5 to 10.0
    trend = compute_weekly_trend(runs, baselines)
    assert len(trend) == 1
    assert trend[0]["accurate"] == 2
    assert trend[0]["accuracy_pct"] == 66.7


def test_weekly_trend_skips_no_size():
    """Runs without size are excluded from weekly trend."""
    runs = [
        make_run(1, "S", 4.0, started_at="2026-02-09T10:00:00Z"),
        make_run(2, None, 4.0, started_at="2026-02-09T10:00:00Z"),
    ]
    baselines, _ = compute_size_baselines(runs)
    trend = compute_weekly_trend(runs, baselines)
    assert len(trend) == 1
    assert trend[0]["total"] == 1


def test_empty_runs():
    """All functions handle empty input gracefully."""
    baselines, size_costs = compute_size_baselines([])
    assert baselines == {}
    rates = compute_accuracy_rates(size_costs, baselines)
    assert rates == {}
    oversized, undersized = detect_mismatches([], baselines)
    assert oversized == []
    assert undersized == []
    trend = compute_weekly_trend([], baselines)
    assert trend == []


def test_single_run_per_size():
    """Single run per size gives 100% accuracy (cost equals median)."""
    runs = [make_run(1, "XS", 1.5)]
    baselines, size_costs = compute_size_baselines(runs)
    rates = compute_accuracy_rates(size_costs, baselines)
    assert rates["XS"]["accuracy_pct"] == 100.0
    assert rates["XS"]["within_range"] == 1


def test_all_sizes_represented():
    """Baselines cover all size brackets when data exists."""
    runs = [
        make_run(1, "XS", 1.0),
        make_run(2, "S", 3.0),
        make_run(3, "M", 7.0),
        make_run(4, "L", 12.0),
        make_run(5, "XL", 20.0),
    ]
    baselines, _ = compute_size_baselines(runs)
    assert set(baselines.keys()) == {"XS", "S", "M", "L", "XL"}


# ---------------------------------------------------------------------------
# Standalone runner
# ---------------------------------------------------------------------------

if __name__ == "__main__":
    test_funcs = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    passed = failed = 0
    for fn in test_funcs:
        try:
            fn()
            passed += 1
            print(f"  PASS  {fn.__name__}")
        except AssertionError as e:
            failed += 1
            print(f"  FAIL  {fn.__name__}: {e}")
    print(f"\n{passed} passed, {failed} failed, {passed + failed} total")
    if failed:
        exit(1)
