/**
 * Dashboard.modeMismatch.test.ts
 *
 * Issue #3218 — Mode-mismatch advisory renderer tests.
 * - Advisory hidden when data is null (≥70% recent runs match active mode).
 * - Advisory text matches AC4 phrasing exactly when threshold is breached.
 */

import { describe, it, expect } from "vitest";
import {
  getModeMismatchAdvisoryHtml,
  type ModeMismatchAdvisoryData,
} from "../../../src/views/dashboard/DashboardHtml";

describe("getModeMismatchAdvisoryHtml", () => {
  it("returns empty string when data is null (no mismatch)", () => {
    expect(getModeMismatchAdvisoryHtml(null)).toBe("");
  });

  it("renders advisory with AC4 phrasing when active mode is in the minority", () => {
    const data: ModeMismatchAdvisoryData = {
      activeMode: "maximum",
      dominantMode: "efficiency",
      dominantCount: 3,
      windowSize: 10,
    };
    const html = getModeMismatchAdvisoryHtml(data);
    expect(html).toContain(
      "3 of your last 10 runs used Efficiency mode; current mode is Maximum — comparing trends?"
    );
    expect(html).toContain('role="status"');
  });

  it("uses the dominant mode label, not the active mode, in the advisory text", () => {
    const data: ModeMismatchAdvisoryData = {
      activeMode: "efficiency",
      dominantMode: "elevated",
      dominantCount: 7,
      windowSize: 10,
    };
    const html = getModeMismatchAdvisoryHtml(data);
    expect(html).toContain("7 of your last 10 runs used Elevated mode");
    expect(html).toContain("current mode is Efficiency");
  });

  it("scales correctly with smaller windows (<10 runs)", () => {
    const data: ModeMismatchAdvisoryData = {
      activeMode: "elevated",
      dominantMode: "maximum",
      dominantCount: 4,
      windowSize: 5,
    };
    const html = getModeMismatchAdvisoryHtml(data);
    expect(html).toContain("4 of your last 5 runs used Maximum mode");
    expect(html).toContain("current mode is Elevated");
  });
});
