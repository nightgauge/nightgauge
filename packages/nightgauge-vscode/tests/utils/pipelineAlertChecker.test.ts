/**
 * Unit tests for pipelineAlertChecker
 *
 * @see pipelineAlertChecker.ts
 * @see Issue #1048 - Automated cost/duration alerting
 * @see Issue #1335 - Replace flat cost threshold with ratio-based anomaly detection
 */

import { describe, it, expect } from "vitest";
import { checkPipelineAlerts, type AlertThresholds } from "../../src/utils/pipelineAlertChecker";

const DEFAULT_THRESHOLDS: AlertThresholds = {
  enabled: true,
  cost_anomaly_ratio: 2.0,
  cost_anomaly_min_usd: 3.0,
  duration_threshold_minutes: 32,
};

describe("checkPipelineAlerts", () => {
  it("returns no alerts when both metrics are below thresholds", () => {
    // estimated $10, actual $15 — ratio 1.5× < 2.0× → no cost alert
    const result = checkPipelineAlerts({
      issueNumber: 100,
      costUsd: 15,
      estimatedCostUsd: 10,
      durationMinutes: 15,
      thresholds: DEFAULT_THRESHOLDS,
    });

    expect(result.costExceeded).toBe(false);
    expect(result.durationExceeded).toBe(false);
    expect(result.alerts).toHaveLength(0);
  });

  it("returns cost alert when cost exceeds ratio threshold", () => {
    // estimated $5, actual $15 — ratio 3× > 2.0× AND $15 > $3 → alert
    const result = checkPipelineAlerts({
      issueNumber: 42,
      costUsd: 15,
      estimatedCostUsd: 5,
      durationMinutes: 10,
      thresholds: DEFAULT_THRESHOLDS,
    });

    expect(result.costExceeded).toBe(true);
    expect(result.durationExceeded).toBe(false);
    expect(result.alerts).toHaveLength(1);
    expect(result.alerts[0].type).toBe("cost");
    expect(result.alerts[0].actual).toBe(15);
    expect(result.alerts[0].threshold).toBe(10); // 5 × 2.0
    expect(result.alerts[0].estimatedCost).toBe(5);
    expect(result.alerts[0].issueNumber).toBe(42);
    expect(result.alerts[0].message).toContain("#42");
    expect(result.alerts[0].message).toContain("$15.00");
    expect(result.alerts[0].message).toContain("2×");
  });

  it("returns duration alert when duration exceeds threshold", () => {
    const result = checkPipelineAlerts({
      issueNumber: 99,
      costUsd: 10,
      estimatedCostUsd: 8,
      durationMinutes: 50,
      thresholds: DEFAULT_THRESHOLDS,
    });

    expect(result.costExceeded).toBe(false);
    expect(result.durationExceeded).toBe(true);
    expect(result.alerts).toHaveLength(1);
    expect(result.alerts[0].type).toBe("duration");
    expect(result.alerts[0].actual).toBe(50);
    expect(result.alerts[0].threshold).toBe(32);
    expect(result.alerts[0].issueNumber).toBe(99);
    expect(result.alerts[0].message).toContain("50.0min");
    expect(result.alerts[0].message).toContain("32min");
  });

  it("returns both alerts when both thresholds exceeded", () => {
    // estimated $5, actual $20 — ratio 4× > 2.0× AND $20 > $3 → cost alert
    const result = checkPipelineAlerts({
      issueNumber: 7,
      costUsd: 20,
      estimatedCostUsd: 5,
      durationMinutes: 60,
      thresholds: DEFAULT_THRESHOLDS,
    });

    expect(result.costExceeded).toBe(true);
    expect(result.durationExceeded).toBe(true);
    expect(result.alerts).toHaveLength(2);
    expect(result.alerts[0].type).toBe("cost");
    expect(result.alerts[1].type).toBe("duration");
  });

  it("returns no alerts when alerting is disabled", () => {
    const result = checkPipelineAlerts({
      issueNumber: 1,
      costUsd: 999,
      estimatedCostUsd: 10,
      durationMinutes: 999,
      thresholds: { ...DEFAULT_THRESHOLDS, enabled: false },
    });

    expect(result.costExceeded).toBe(false);
    expect(result.durationExceeded).toBe(false);
    expect(result.alerts).toHaveLength(0);
  });

  it("does not alert when values exactly equal thresholds", () => {
    // estimated $10, actual $20 — ratio exactly 2.0× → no alert (not strictly greater)
    const result = checkPipelineAlerts({
      issueNumber: 1,
      costUsd: 20,
      estimatedCostUsd: 10,
      durationMinutes: 32,
      thresholds: DEFAULT_THRESHOLDS,
    });

    expect(result.costExceeded).toBe(false);
    expect(result.durationExceeded).toBe(false);
    expect(result.alerts).toHaveLength(0);
  });

  it("respects custom threshold values", () => {
    // estimated $2, actual $5 — ratio 2.5× > 1.5× AND $5 > $1 → alert
    const result = checkPipelineAlerts({
      issueNumber: 1,
      costUsd: 5,
      estimatedCostUsd: 2,
      durationMinutes: 3,
      thresholds: {
        enabled: true,
        cost_anomaly_ratio: 1.5,
        cost_anomaly_min_usd: 1.0,
        duration_threshold_minutes: 1,
      },
    });

    expect(result.costExceeded).toBe(true);
    expect(result.durationExceeded).toBe(true);
    expect(result.alerts).toHaveLength(2);
  });

  // ── Required acceptance criteria tests (Issue #1335) ──────────────────────

  it("Opus high-cost run does NOT alert when cost is within ratio of estimated", () => {
    // Opus/L issue estimated at $30, actual $40 — ratio 1.33× < 2.0× → no alert
    const result = checkPipelineAlerts({
      issueNumber: 100,
      costUsd: 40,
      estimatedCostUsd: 30,
      durationMinutes: 15,
      thresholds: DEFAULT_THRESHOLDS, // ratio=2.0
    });
    expect(result.costExceeded).toBe(false);
  });

  it("Sonnet anomaly alerts when cost exceeds ratio threshold", () => {
    // Sonnet/S issue estimated at $2, actual $10 — ratio 5× > 2.0× AND $10 > $3 → alert
    const result = checkPipelineAlerts({
      issueNumber: 42,
      costUsd: 10,
      estimatedCostUsd: 2,
      durationMinutes: 5,
      thresholds: DEFAULT_THRESHOLDS,
    });
    expect(result.costExceeded).toBe(true);
    expect(result.alerts[0].type).toBe("cost");
  });

  it("below min_usd does NOT alert even when ratio exceeds threshold", () => {
    // Tiny run: estimated $0.50, actual $1.50 — ratio 3× > 2.0× BUT $1.50 < $3 min → no alert
    const result = checkPipelineAlerts({
      issueNumber: 7,
      costUsd: 1.5,
      estimatedCostUsd: 0.5,
      durationMinutes: 2,
      thresholds: DEFAULT_THRESHOLDS,
    });
    expect(result.costExceeded).toBe(false);
  });
});
