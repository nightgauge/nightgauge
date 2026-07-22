/**
 * Unit tests for tier logic in settings panel
 *
 * Tests source tracking, tier badge generation, and tier-specific operations.
 *
 * @see Issue #440 - Multi-tier config GUI support
 */

import { describe, it, expect } from "vitest";
import {
  getTierBadgeHtml,
  getTierBadgeStyles,
  getTierChainHtml,
  getResetOptions,
  TIER_BADGE_LABELS,
  TIER_BADGE_COLORS,
  TIER_BADGE_ICONS,
  TIER_BADGE_TOOLTIPS,
  sourceToViewTier,
  UX_TIER_LABELS,
  UX_TIER_TOOLTIPS,
  techTierToUxTier,
} from "../TierBadge";
import type { ViewTier } from "../types";
import { TIER_TABS } from "../types";

describe("TierBadge", () => {
  describe("TIER_BADGE_LABELS", () => {
    it("has labels for all tiers", () => {
      expect(TIER_BADGE_LABELS.merged).toBe("Merged");
      expect(TIER_BADGE_LABELS.default).toBe("Default");
      expect(TIER_BADGE_LABELS.global).toBe("Global");
      expect(TIER_BADGE_LABELS.project).toBe("Project");
      expect(TIER_BADGE_LABELS.local).toBe("Local");
      expect(TIER_BADGE_LABELS.env).toBe("Env");
      expect(TIER_BADGE_LABELS.cli).toBe("CLI");
    });
  });

  describe("TIER_BADGE_COLORS", () => {
    it("has colors for all tiers", () => {
      const tiers: Array<ViewTier | "cli"> = [
        "merged",
        "default",
        "global",
        "project",
        "local",
        "env",
        "cli",
      ];

      for (const tier of tiers) {
        expect(TIER_BADGE_COLORS[tier]).toBeDefined();
        expect(TIER_BADGE_COLORS[tier].bg).toMatch(/var\(--vscode-/);
        expect(TIER_BADGE_COLORS[tier].fg).toMatch(/var\(--vscode-/);
      }
    });
  });

  describe("TIER_BADGE_ICONS", () => {
    it("has icons for all tiers", () => {
      expect(TIER_BADGE_ICONS.merged).toBe("layers");
      expect(TIER_BADGE_ICONS.project).toBe("folder");
      expect(TIER_BADGE_ICONS.local).toBe("person");
      expect(TIER_BADGE_ICONS.global).toBe("home");
      expect(TIER_BADGE_ICONS.env).toBe("terminal");
    });
  });

  describe("getTierBadgeHtml", () => {
    it("generates badge HTML with correct class", () => {
      const html = getTierBadgeHtml("project");
      expect(html).toContain("tier-badge");
      expect(html).toContain("tier-badge-project");
      expect(html).toContain("Project");
    });

    it("includes tooltip from TIER_BADGE_TOOLTIPS", () => {
      const html = getTierBadgeHtml("local");
      expect(html).toContain("title=");
      expect(html).toContain("gitignored");
    });

    it("shows env var name in tooltip when provided", () => {
      const html = getTierBadgeHtml("env", {
        envVarName: "NIGHTGAUGE_PIPELINE_AUTO_FIX",
      });
      expect(html).toContain("NIGHTGAUGE_PIPELINE_AUTO_FIX");
    });

    it("shows icon when showIcon is true", () => {
      const html = getTierBadgeHtml("project", { showIcon: true });
      expect(html).toContain("codicon");
      expect(html).toContain("codicon-folder");
    });

    it("compact mode shows only icon", () => {
      const html = getTierBadgeHtml("project", { compact: true });
      expect(html).toContain("codicon-folder");
      // Label should not be present in compact mode
      expect(html).not.toMatch(/>Project</);
    });
  });

  describe("getTierBadgeStyles", () => {
    it("includes CSS for tier badges", () => {
      const styles = getTierBadgeStyles();
      expect(styles).toContain(".tier-badge");
      expect(styles).toContain(".tier-badge-default");
      expect(styles).toContain(".tier-badge-env");
    });
  });

  describe("getTierChainHtml", () => {
    it("returns empty for no tiers", () => {
      expect(getTierChainHtml([])).toBe("");
    });

    it("returns single badge for one tier", () => {
      const html = getTierChainHtml(["project"]);
      expect(html).toContain("tier-badge");
    });

    it("shows override chain for multiple tiers", () => {
      const html = getTierChainHtml(["default", "project", "local"]);
      expect(html).toContain("tier-chain");
      expect(html).toContain("→");
      // Earlier tiers should be struck through
      expect(html).toContain("line-through");
    });
  });

  describe("getResetOptions", () => {
    it("offers reset to default when not already default", () => {
      const options = getResetOptions("project", ["default", "project"]);
      expect(options).toContainEqual(
        expect.objectContaining({
          tier: "default",
          label: "Reset to default",
        })
      );
    });

    it("does not offer reset to default when already default", () => {
      const options = getResetOptions("default", ["default"]);
      expect(options).not.toContainEqual(expect.objectContaining({ tier: "default" }));
    });

    it("offers reset to global when project overrides it", () => {
      const options = getResetOptions("project", ["default", "global", "project"]);
      expect(options).toContainEqual(
        expect.objectContaining({
          tier: "global",
          label: "Reset to global",
        })
      );
    });

    it("offers reset to project when local overrides it", () => {
      const options = getResetOptions("local", ["default", "project", "local"]);
      expect(options).toContainEqual(
        expect.objectContaining({
          tier: "project",
          label: "Reset to project",
        })
      );
    });
  });

  describe("sourceToViewTier", () => {
    it("converts ConfigSource to ViewTier", () => {
      expect(sourceToViewTier("default")).toBe("default");
      expect(sourceToViewTier("global")).toBe("global");
      expect(sourceToViewTier("project")).toBe("project");
      expect(sourceToViewTier("local")).toBe("local");
      expect(sourceToViewTier("env")).toBe("env");
      expect(sourceToViewTier("cli")).toBe("cli");
    });
  });
});

describe("TIER_TABS", () => {
  it("has correct number of tabs", () => {
    expect(TIER_TABS).toHaveLength(5); // merged, project, local, global, env
  });

  it("merged tab is first and not editable", () => {
    const mergedTab = TIER_TABS[0];
    expect(mergedTab.id).toBe("merged");
    expect(mergedTab.editable).toBe(false);
  });

  it("project tab is editable", () => {
    const projectTab = TIER_TABS.find((t) => t.id === "project");
    expect(projectTab).toBeDefined();
    expect(projectTab?.editable).toBe(true);
    expect(projectTab?.filePath).toBe(".nightgauge/config.yaml");
  });

  it("local tab is editable", () => {
    const localTab = TIER_TABS.find((t) => t.id === "local");
    expect(localTab).toBeDefined();
    expect(localTab?.editable).toBe(true);
    expect(localTab?.filePath).toBe(".nightgauge/config.local.yaml");
  });

  it("global tab is editable (machine tier)", () => {
    // #3997 — the Global tab edits ~/.nightgauge/config.yaml so machine-tier
    // keys (e.g. the platform license key) can be saved through the UI.
    const globalTab = TIER_TABS.find((t) => t.id === "global");
    expect(globalTab).toBeDefined();
    expect(globalTab?.editable).toBe(true);
    expect(globalTab?.filePath).toBe("~/.nightgauge/config.yaml");
  });

  it("env tab is read-only and has no filePath", () => {
    const envTab = TIER_TABS.find((t) => t.id === "env");
    expect(envTab).toBeDefined();
    expect(envTab?.editable).toBe(false);
    expect(envTab?.filePath).toBeUndefined();
  });

  it("all tabs have required properties", () => {
    for (const tab of TIER_TABS) {
      expect(tab.id).toBeDefined();
      expect(tab.label).toBeDefined();
      expect(tab.icon).toBeDefined();
      expect(tab.description).toBeDefined();
      expect(typeof tab.editable).toBe("boolean");
    }
  });
});

describe("UX_TIER_LABELS (Issue #3339)", () => {
  it("defines all three UX tier labels", () => {
    expect(UX_TIER_LABELS.team).toBe("Team");
    expect(UX_TIER_LABELS.you).toBe("You");
    expect(UX_TIER_LABELS["this-run"]).toBe("This run");
  });
});

describe("UX_TIER_TOOLTIPS (Issue #3339)", () => {
  it("Team tooltip references config.yaml and git", () => {
    expect(UX_TIER_TOOLTIPS.team).toContain("config.yaml");
    expect(UX_TIER_TOOLTIPS.team).toContain("git");
  });

  it("You tooltip references home directory config", () => {
    expect(UX_TIER_TOOLTIPS.you).toContain("~/.nightgauge");
  });

  it("This-run tooltip references ephemeral state", () => {
    expect(UX_TIER_TOOLTIPS["this-run"]).toContain("never committed");
  });
});

describe("techTierToUxTier (Issue #3339)", () => {
  it("correctly maps all technical tiers", () => {
    expect(techTierToUxTier("project")).toBe("team");
    expect(techTierToUxTier("global")).toBe("you");
    expect(techTierToUxTier("local")).toBe("this-run");
    expect(techTierToUxTier("env")).toBe("this-run");
    expect(techTierToUxTier("cli")).toBe("this-run");
    expect(techTierToUxTier("merged")).toBeNull();
    expect(techTierToUxTier("default")).toBeNull();
  });
});

describe("TierViewState type", () => {
  it("has correct shape", () => {
    // Type test - this will fail to compile if TierViewState is wrong
    const state: import("../types").TierViewState = {
      currentTier: "merged",
      defaultEditTier: "project",
      hasGlobalConfig: true,
      hasLocalConfig: false,
      hasProjectConfig: true,
      activeEnvVars: ["NIGHTGAUGE_PIPELINE_AUTO_FIX"],
    };

    expect(state.currentTier).toBe("merged");
    expect(state.defaultEditTier).toBe("project");
  });
});
