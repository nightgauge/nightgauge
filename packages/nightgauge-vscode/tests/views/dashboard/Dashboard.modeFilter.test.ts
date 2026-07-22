/**
 * Dashboard.modeFilter.test.ts
 *
 * Issue #3218 — Mode-filter chip rendering tests.
 * - Chip group renders all four buttons (all + 3 modes).
 * - The "active" class follows the currentMode argument.
 * - Click handler dispatch is wired via data-mode attributes.
 */

import { describe, it, expect } from "vitest";
import { getModeFilterToggleHtml } from "../../../src/views/dashboard/DashboardHtml";

describe("getModeFilterToggleHtml", () => {
  it("renders all four chip buttons (all + efficiency + elevated + maximum)", () => {
    const html = getModeFilterToggleHtml("all");
    expect(html).toContain('data-mode="all"');
    expect(html).toContain('data-mode="efficiency"');
    expect(html).toContain('data-mode="elevated"');
    expect(html).toContain('data-mode="maximum"');
    // Four toggle-btn elements.
    const matches = html.match(/class="toggle-btn[^"]*"/g) ?? [];
    expect(matches.length).toBe(4);
  });

  it('marks the "all" button active when currentMode="all"', () => {
    const html = getModeFilterToggleHtml("all");
    // Active class is on the all-mode button.
    expect(html).toMatch(/class="toggle-btn active" data-mode="all"/);
    // Other buttons are not active.
    expect(html).toMatch(/class="toggle-btn " data-mode="efficiency"/);
    expect(html).toMatch(/class="toggle-btn " data-mode="elevated"/);
    expect(html).toMatch(/class="toggle-btn " data-mode="maximum"/);
  });

  it('marks the "efficiency" button active when currentMode="efficiency"', () => {
    const html = getModeFilterToggleHtml("efficiency");
    expect(html).toMatch(/class="toggle-btn active" data-mode="efficiency"/);
    expect(html).toMatch(/class="toggle-btn " data-mode="all"/);
  });

  it('marks the "maximum" button active when currentMode="maximum"', () => {
    const html = getModeFilterToggleHtml("maximum");
    expect(html).toMatch(/class="toggle-btn active" data-mode="maximum"/);
  });

  it("uses the same .scope-toggle/.toggle-btn pattern as the existing scope toggle (AC5)", () => {
    const html = getModeFilterToggleHtml("elevated");
    // Mirrors the existing scope-toggle class for visual consistency — no new
    // styling primitives per AC5. The mode-toggle class is an additional hook
    // for layout adjustments.
    expect(html).toContain("scope-toggle");
    expect(html).toContain("mode-toggle");
    expect(html).toContain('role="group"');
  });
});
