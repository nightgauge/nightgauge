/**
 * UX tier badge tests — Issue #3339
 *
 * Tests the display-only UX tier model (Team / You / This run) that maps
 * the 6-tier technical system to user-friendly labels in the merged view.
 */

import { describe, it, expect } from "vitest";
import {
  UX_TIER_LABELS,
  UX_TIER_COLORS,
  UX_TIER_TOOLTIPS,
  techTierToUxTier,
  getUxTierBadgeHtml,
} from "../../../src/views/settings/TierBadge";

describe("UX_TIER_LABELS", () => {
  it("has the three UX labels", () => {
    expect(UX_TIER_LABELS.team).toBe("Team");
    expect(UX_TIER_LABELS.you).toBe("You");
    expect(UX_TIER_LABELS["this-run"]).toBe("This run");
  });
});

describe("UX_TIER_COLORS", () => {
  it("Team uses green CSS variable", () => {
    expect(UX_TIER_COLORS.team.bg).toContain("green");
    expect(UX_TIER_COLORS.team.bg).toMatch(/var\(--vscode-/);
  });

  it("You uses blue CSS variable", () => {
    expect(UX_TIER_COLORS.you.bg).toContain("blue");
    expect(UX_TIER_COLORS.you.bg).toMatch(/var\(--vscode-/);
  });

  it("This-run uses purple CSS variable", () => {
    expect(UX_TIER_COLORS["this-run"].bg).toContain("purple");
    expect(UX_TIER_COLORS["this-run"].bg).toMatch(/var\(--vscode-/);
  });

  it("all foreground colors use VSCode variables", () => {
    for (const uxTier of ["team", "you", "this-run"] as const) {
      expect(UX_TIER_COLORS[uxTier].fg).toMatch(/var\(--vscode-/);
    }
  });
});

describe("UX_TIER_TOOLTIPS", () => {
  it("Team tooltip mentions committed to git", () => {
    expect(UX_TIER_TOOLTIPS.team).toContain("committed to git");
    expect(UX_TIER_TOOLTIPS.team).toContain(".nightgauge/config.yaml");
    expect(UX_TIER_TOOLTIPS.team).toContain("Edit team config");
  });

  it("You tooltip mentions machine only", () => {
    expect(UX_TIER_TOOLTIPS.you).toContain("machine only");
    expect(UX_TIER_TOOLTIPS.you).toContain("~/.nightgauge/config.yaml");
  });

  it("This-run tooltip mentions never committed", () => {
    expect(UX_TIER_TOOLTIPS["this-run"]).toContain("never committed");
  });
});

describe("techTierToUxTier", () => {
  it("maps project to team", () => {
    expect(techTierToUxTier("project")).toBe("team");
  });

  it("maps global to you", () => {
    expect(techTierToUxTier("global")).toBe("you");
  });

  it("maps local to this-run", () => {
    expect(techTierToUxTier("local")).toBe("this-run");
  });

  it("maps env to this-run", () => {
    expect(techTierToUxTier("env")).toBe("this-run");
  });

  it("maps cli to this-run", () => {
    expect(techTierToUxTier("cli")).toBe("this-run");
  });

  it("returns null for merged", () => {
    expect(techTierToUxTier("merged")).toBeNull();
  });

  it("returns null for default", () => {
    expect(techTierToUxTier("default")).toBeNull();
  });
});

describe("getUxTierBadgeHtml", () => {
  it("project renders Team badge with correct classes", () => {
    const html = getUxTierBadgeHtml("project");
    expect(html).toContain("Team");
    expect(html).toContain("tier-badge-ux-team");
    expect(html).toContain("tier-badge-ux");
    expect(html).toContain("tier-badge");
  });

  it("project badge has aria-label including committed to git", () => {
    const html = getUxTierBadgeHtml("project");
    expect(html).toContain("aria-label=");
    expect(html).toContain("committed to git");
  });

  it("global renders You badge", () => {
    const html = getUxTierBadgeHtml("global");
    expect(html).toContain("You");
    expect(html).toContain("tier-badge-ux-you");
  });

  it("global badge has aria-label including machine only", () => {
    const html = getUxTierBadgeHtml("global");
    expect(html).toContain("machine only");
  });

  it("local renders This run badge", () => {
    const html = getUxTierBadgeHtml("local");
    expect(html).toContain("This run");
    expect(html).toContain("tier-badge-ux-this-run");
  });

  it("env renders This run badge (same as local)", () => {
    const html = getUxTierBadgeHtml("env");
    expect(html).toContain("This run");
    expect(html).toContain("tier-badge-ux-this-run");
  });

  it("cli renders This run badge", () => {
    const html = getUxTierBadgeHtml("cli");
    expect(html).toContain("This run");
  });

  it("default returns empty string (no badge)", () => {
    expect(getUxTierBadgeHtml("default")).toBe("");
  });

  it("merged returns empty string (no badge)", () => {
    expect(getUxTierBadgeHtml("merged")).toBe("");
  });

  it("badge uses inline color style from UX_TIER_COLORS", () => {
    const html = getUxTierBadgeHtml("project");
    expect(html).toContain("var(--vscode-charts-green)");
  });

  it("badge has tooltip from UX_TIER_TOOLTIPS", () => {
    const html = getUxTierBadgeHtml("project");
    expect(html).toContain("title=");
    expect(html).toContain("config.yaml");
  });
});

describe("SettingsHtml UX tier integration", () => {
  it("getSettingsHtml merged view contains tier info banner", async () => {
    const { getSettingsHtml } = await import("../../../src/views/settings/SettingsHtml");
    const { getDefaultConfig } = await import("../../../src/config/schema");
    const config = getDefaultConfig() as any;
    const html = getSettingsHtml(
      { cspSource: "test-csp" } as any,
      config,
      new Set(),
      {},
      {
        currentTier: "merged",
        defaultEditTier: "project",
        hasGlobalConfig: false,
        hasLocalConfig: false,
        hasProjectConfig: true,
        activeEnvVars: [],
      }
    );
    expect(html).toContain("tier-info-banner");
    expect(html).toContain("Learn more");
    expect(html).toContain("docs/CONFIGURATION.md");
  });

  it("getSettingsHtml non-merged view does NOT render tier info banner element", async () => {
    const { getSettingsHtml } = await import("../../../src/views/settings/SettingsHtml");
    const { getDefaultConfig } = await import("../../../src/config/schema");
    const config = getDefaultConfig() as any;
    const html = getSettingsHtml(
      { cspSource: "test-csp" } as any,
      config,
      new Set(),
      {},
      {
        currentTier: "project",
        defaultEditTier: "project",
        hasGlobalConfig: false,
        hasLocalConfig: false,
        hasProjectConfig: true,
        activeEnvVars: [],
      }
    );
    // The banner div element should not be present; only CSS/JS references remain
    expect(html).not.toContain('class="tier-info-banner"');
    expect(html).not.toContain('role="note"');
  });

  it("merged view with project-source setting renders Edit team config button", async () => {
    const { getSettingsHtml } = await import("../../../src/views/settings/SettingsHtml");
    const { getDefaultConfig } = await import("../../../src/config/schema");
    const config = getDefaultConfig() as any;
    const sources: Record<string, string> = { "ui.core.adapter": "project" };
    const html = getSettingsHtml(
      { cspSource: "test-csp" } as any,
      config,
      new Set(),
      sources as any,
      {
        currentTier: "merged",
        defaultEditTier: "project",
        hasGlobalConfig: false,
        hasLocalConfig: false,
        hasProjectConfig: true,
        activeEnvVars: [],
      }
    );
    expect(html).toContain("edit-team-config-btn");
    expect(html).toContain("Edit team config");
  });
});
