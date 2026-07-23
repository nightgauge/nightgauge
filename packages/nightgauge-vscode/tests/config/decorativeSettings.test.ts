import { describe, expect, it } from "vitest";
import { IncrediConfigSchema, getDefaultConfig } from "../../src/config/schema";
import { getSettingsHtml } from "../../src/views/settings/SettingsHtml";

describe("deprecated decorative settings", () => {
  it("accepts legacy files without advertising settings that have no consumer", () => {
    expect(() =>
      IncrediConfigSchema.parse({
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
