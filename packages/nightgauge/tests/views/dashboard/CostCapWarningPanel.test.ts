/**
 * CostCapWarningPanel.test.ts
 *
 * Unit + snapshot tests for `getCostCapWarningTableHtml()` (Issue #3276).
 */

import { describe, it, expect } from "vitest";
import {
  getCostCapWarningTableHtml,
  type CostCapWarningRow,
} from "../../../src/views/dashboard/tabs/CostTabHtml";

const tightRow: CostCapWarningRow = {
  stage: "feature-dev",
  effectiveCap: 5,
  historicalMedian: 20,
  threshold: 24,
  multiplier: 1.2,
  capEnvKey: "NIGHTGAUGE_PIPELINE_STAGE_COST_CAP_FEATURE_DEV",
  capConfigPath: "pipeline.stage_cost_caps.feature-dev",
  isTight: true,
  warnThresholdUsd: 18,
  ceilingUsd: 75,
};

const okRow: CostCapWarningRow = {
  stage: "pr-create",
  effectiveCap: 30,
  historicalMedian: 20,
  threshold: 24,
  multiplier: 1.2,
  capEnvKey: "NIGHTGAUGE_PIPELINE_STAGE_COST_CAP_PR_CREATE",
  capConfigPath: "pipeline.stage_cost_caps.pr-create",
  isTight: false,
  warnThresholdUsd: 18,
  ceilingUsd: 90,
};

const noHistoryRow: CostCapWarningRow = {
  stage: "issue-pickup",
  effectiveCap: 1,
  historicalMedian: 0,
  threshold: 0,
  multiplier: 1.2,
  capEnvKey: "NIGHTGAUGE_PIPELINE_STAGE_COST_CAP_ISSUE_PICKUP",
  capConfigPath: "pipeline.stage_cost_caps.issue-pickup",
  isTight: false,
  warnThresholdUsd: 0,
  ceilingUsd: 0,
};

describe("getCostCapWarningTableHtml", () => {
  it("returns empty string for empty rows array", () => {
    expect(getCostCapWarningTableHtml([])).toBe("");
  });

  it("renders tight cap row with warning class and badge", () => {
    const html = getCostCapWarningTableHtml([tightRow]);
    expect(html).toContain('data-is-tight="true"');
    expect(html).toContain('class="cost-cap-tight"');
    expect(html).toContain("⚠ Too tight");
    expect(html).toContain("$5.00");
    expect(html).toContain("$20.00");
    expect(html).toContain("$24.00");
  });

  it("renders ok row without warning class", () => {
    const html = getCostCapWarningTableHtml([okRow]);
    expect(html).toContain('data-is-tight="false"');
    expect(html).not.toContain('class="cost-cap-tight"');
    expect(html).toContain("✓ OK");
  });

  it("renders dash for median and threshold when historicalMedian is 0", () => {
    const html = getCostCapWarningTableHtml([noHistoryRow]);
    expect(html).toContain("—");
    expect(html).not.toContain("$0.00");
  });

  it("sets data-stage attribute correctly", () => {
    const html = getCostCapWarningTableHtml([tightRow]);
    expect(html).toContain('data-stage="feature-dev"');
  });

  it("renders table with stage name formatted", () => {
    const html = getCostCapWarningTableHtml([tightRow]);
    // formatStageName converts 'feature-dev' to a readable label
    expect(html).toContain("feature-dev");
  });

  it("snapshot — tight cap row", () => {
    expect(getCostCapWarningTableHtml([tightRow])).toMatchSnapshot();
  });

  it("snapshot — ok row", () => {
    expect(getCostCapWarningTableHtml([okRow])).toMatchSnapshot();
  });

  it("snapshot — no history row (dash values)", () => {
    expect(getCostCapWarningTableHtml([noHistoryRow])).toMatchSnapshot();
  });

  it("snapshot — mixed rows", () => {
    expect(getCostCapWarningTableHtml([tightRow, okRow, noHistoryRow])).toMatchSnapshot();
  });
});
