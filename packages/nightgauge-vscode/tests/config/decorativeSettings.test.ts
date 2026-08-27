import { describe, expect, it } from "vitest";
import {
  DEFAULT_CONFIG,
  NightgaugeConfigSchema,
  SanitizationConfigSchema,
  getDefaultConfig,
  mergeWithDefaults,
  resolveSanitizationMode,
} from "../../src/config/schema";
import { getSettingsHtml } from "../../src/views/settings/SettingsHtml";
import { NightgaugeYamlService } from "../../src/views/settings/NightgaugeYamlService";
import { getDashboardHtml } from "../../src/views/dashboard/DashboardHtml";
import { makeEmptyAggregates } from "../views/dashboard/fixtures/aggregates";

describe("deprecated decorative settings", () => {
  it("accepts legacy files without advertising settings that have no consumer", () => {
    expect(() =>
      NightgaugeConfigSchema.parse({
        pr: { draft_by_default: true },
        branch: { prefixes: { feature: "feature/" } },
        issue: { auto_assign: true, default_labels: ["triage"] },
      })
    ).not.toThrow();

    const html = getSettingsHtml(
      { cspSource: "test" } as never,
      getDefaultConfig(),
      new Set(),
      {},
      {
        currentTier: "project",
        defaultEditTier: "local",
        hasGlobalConfig: false,
        hasLocalConfig: false,
        hasProjectConfig: true,
        activeEnvVars: [],
      }
    );
    for (const path of [
      "pull_request.draft_by_default",
      "branch.prefixes.feature",
      "issue.auto_assign",
      "issue.default_labels",
    ]) {
      expect(html).not.toContain(path);
    }
    expect(html).toContain("issue.default_status");
  });
});

/**
 * Issue #967 — the sanitization surface must expose exactly one key.
 *
 * Six of the seven `sanitization.*` settings were decoration: the Go
 * enforcement gate reads only `sanitization.mode`. These assertions are
 * symbol- and render-level on purpose — a substring grep over the sources
 * could not go red for `Dashboard.ts` (optional chaining, no dotted literal)
 * or for a key re-added to a non-strict `z.object` (unknown keys parse fine).
 */
describe("sanitization surface (Issue #967)", () => {
  it("declares exactly one schema field: mode", () => {
    expect(Object.keys(SanitizationConfigSchema.shape).sort()).toEqual(["mode"]);
  });

  it("carries nothing dead in the defaults", () => {
    expect(DEFAULT_CONFIG.sanitization).toEqual({ mode: "warn" });
    expect(Object.keys(mergeWithDefaults({}).sanitization ?? {}).sort()).toEqual(["mode"]);
  });

  it("resolves the mode from the mode field alone", () => {
    expect(resolveSanitizationMode(undefined)).toBe("warn");
    expect(resolveSanitizationMode({})).toBe("warn");
    expect(resolveSanitizationMode({ mode: "warn" })).toBe("warn");
    expect(resolveSanitizationMode({ mode: "block" })).toBe("block");
    expect(resolveSanitizationMode({ mode: "disabled" })).toBe("disabled");
    // The deprecated warn_only leg is gone: it used to make `false` mean block.
    expect(resolveSanitizationMode({ warn_only: false } as never)).toBe("warn");
    expect(resolveSanitizationMode({ warn_only: true } as never)).toBe("warn");
  });

  it("renders exactly one sanitization control, the mode selector", () => {
    const html = getSettingsHtml(
      { cspSource: "test" } as never,
      getDefaultConfig(),
      new Set(),
      {},
      {
        currentTier: "project",
        defaultEditTier: "local",
        hasGlobalConfig: false,
        hasLocalConfig: false,
        hasProjectConfig: true,
        activeEnvVars: [],
      }
    );
    const paths = [...html.matchAll(/\sdata-path="([^"]+)"/g)].map((m) => m[1]);
    expect(paths.filter((p) => p.startsWith("sanitization.")).sort()).toEqual([
      "sanitization.mode",
    ]);
  });

  it("has no allowlist-suggestion service module", async () => {
    const modulePath = "../../src/services/AllowlistSuggestionService";
    await expect(import(/* @vite-ignore */ modulePath)).rejects.toThrow();
  });

  it("has no sanitization writers on NightgaugeYamlService", () => {
    expect(NightgaugeYamlService.prototype).not.toHaveProperty("addToSanitizationAllowlist");
    expect(NightgaugeYamlService.prototype).not.toHaveProperty("addToSanitizationSafeDirectories");
  });

  it("emits no allowlist-suggestion messages from the dashboard webview", () => {
    const html = getDashboardHtml(
      { cspSource: "test-csp" } as never,
      null,
      [],
      makeEmptyAggregates(),
      {
        pipelineStart: 0,
        issuePickup: 5,
        featurePlanning: 30,
        featureDev: 120,
        featureValidate: 15,
        prCreate: 10,
        prMerge: 5,
        pipelineFinish: 0,
      }
    );
    expect(html).not.toContain("firewallAddAllowlist");
    expect(html).not.toContain("firewallDismissSuggestion");
  });
});
