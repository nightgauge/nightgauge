/**
 * StallThresholdPanel.test.ts
 *
 * Issue #3218 — Stall threshold table renderer tests.
 * - Renders one row per (stage, mode) input.
 * - Falls back to "static" source and renders "—" when the cell has no data.
 * - Marks cold-start rows with the "disabled" kill threshold and a cold badge.
 * - Returns empty string when given no rows.
 */

import { describe, it, expect } from "vitest";
import {
  getStallThresholdTableHtml,
  type StallThresholdRow,
} from "../../../src/views/dashboard/tabs/PerformanceTabHtml";

describe("getStallThresholdTableHtml", () => {
  it("returns empty string when given no rows", () => {
    expect(getStallThresholdTableHtml([])).toBe("");
  });

  it("renders one row per (stage, mode) input", () => {
    const rows: StallThresholdRow[] = [
      {
        stage: "feature-dev",
        mode: "efficiency",
        size: "all",
        warnSec: 600,
        killSec: 1800,
        source: "calibrated",
        isColdStart: false,
      },
      {
        stage: "feature-dev",
        mode: "elevated",
        size: "all",
        warnSec: 900,
        killSec: 2700,
        source: "calibrated",
        isColdStart: false,
      },
      {
        stage: "feature-dev",
        mode: "maximum",
        size: "all",
        warnSec: 1800,
        killSec: 5400,
        source: "calibrated",
        isColdStart: false,
      },
    ];
    const html = getStallThresholdTableHtml(rows);
    // Three <tr> rows in the body — count by data-stage attributes.
    const matchCount = (html.match(/data-stage="feature-dev"/g) ?? []).length;
    expect(matchCount).toBe(3);
    expect(html).toContain('data-mode="efficiency"');
    expect(html).toContain('data-mode="elevated"');
    expect(html).toContain('data-mode="maximum"');
    expect(html).toContain("600s");
    expect(html).toContain("1800s");
  });

  it('falls back to "static" source and renders "—" for null thresholds', () => {
    const rows: StallThresholdRow[] = [
      {
        stage: "pr-merge",
        mode: "efficiency",
        size: "all",
        warnSec: null,
        killSec: null,
        source: "static",
        isColdStart: false,
      },
    ];
    const html = getStallThresholdTableHtml(rows);
    expect(html).toContain("static");
    // "—" appears in both warn and kill columns when thresholds are absent.
    expect((html.match(/—/g) ?? []).length).toBeGreaterThanOrEqual(2);
  });

  it("marks cold-start rows with disabled kill and a cold badge", () => {
    const rows: StallThresholdRow[] = [
      {
        stage: "feature-validate",
        mode: "elevated",
        size: "all",
        warnSec: 600,
        killSec: 0,
        source: "calibrated",
        isColdStart: true,
      },
    ];
    const html = getStallThresholdTableHtml(rows);
    expect(html).toContain("<em>disabled</em>");
    expect(html).toContain("cold");
  });

  it('always includes a "Size" column rendering "all" (ADR-002 — size keying reserved)', () => {
    const rows: StallThresholdRow[] = [
      {
        stage: "issue-pickup",
        mode: "efficiency",
        size: "all",
        warnSec: 60,
        killSec: 180,
        source: "calibrated",
        isColdStart: false,
      },
    ];
    const html = getStallThresholdTableHtml(rows);
    expect(html).toContain("<th>Size</th>");
    // Cell value rendered after the Mode pill.
    expect(html).toContain(">all<");
  });
});
