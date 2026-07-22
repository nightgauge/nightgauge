import { describe, it, expect } from "vitest";
import { getSettingsHtml } from "../../../src/views/settings/SettingsHtml";
import { getDefaultConfig } from "../../../src/config/schema";
import type { IncrediConfig } from "../../../src/views/settings/types";
import type { ConfigSourceMap } from "../../../src/config/schema";

describe("SettingsHtml ralph_loop section", () => {
  it("renders ralph_loop section controls", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const html = getSettingsHtml({ cspSource: "test-csp" } as any, config);

    expect(html).toContain('id="section-ralph_loop"');

    // General subsection
    expect(html).toContain('data-path="ralph_loop.enabled"');
    expect(html).toContain('data-path="ralph_loop.build"');
    expect(html).toContain('data-path="ralph_loop.tests"');
    expect(html).toContain('data-path="ralph_loop.lint"');

    // Safety Limits subsection
    expect(html).toContain('data-path="ralph_loop.limits.max_iterations"');
    expect(html).toContain('data-path="ralph_loop.limits.token_budget_per_iteration"');
    expect(html).toContain('data-path="ralph_loop.limits.total_token_budget"');
    expect(html).toContain('data-path="ralph_loop.limits.iteration_timeout_ms"');
    expect(html).toContain('data-path="ralph_loop.limits.total_timeout_ms"');

    // Abort Patterns subsection
    expect(html).toContain('data-path="ralph_loop.abort_patterns"');

    // Labels
    expect(html).toContain("Enabled");
    expect(html).toContain("Build Auto-Fix");
    expect(html).toContain("Test Auto-Fix");
    expect(html).toContain("Lint Auto-Fix");
    expect(html).toContain("Max Iterations");
    expect(html).toContain("Token Budget Per Iteration");
    expect(html).toContain("Total Token Budget");
    expect(html).toContain("Iteration Timeout (ms)");
    expect(html).toContain("Total Timeout (ms)");
    expect(html).toContain("Abort Patterns");

    // Subsection titles
    expect(html).toContain("General");
    expect(html).toContain("Safety Limits");
  });

  it("renders correct default values", () => {
    const config = {} as IncrediConfig;
    const html = getSettingsHtml({ cspSource: "test-csp" } as any, config);

    // enabled defaults to true (checked)
    expect(html).toMatch(/id="ralph_loop\.enabled"[^>]*checked/);

    // build defaults to true (checked)
    expect(html).toMatch(/id="ralph_loop\.build"[^>]*checked/);

    // tests defaults to true (checked)
    expect(html).toMatch(/id="ralph_loop\.tests"[^>]*checked/);

    // lint defaults to false (not checked)
    const lintMatch = html.match(/id="ralph_loop\.lint"[^>]*/);
    expect(lintMatch).toBeTruthy();
    expect(lintMatch![0]).not.toContain("checked");

    // max_iterations defaults to 3
    expect(html).toMatch(/id="ralph_loop\.limits\.max_iterations"[^>]*value="3"/);

    // token_budget_per_iteration defaults to 2000
    expect(html).toMatch(/id="ralph_loop\.limits\.token_budget_per_iteration"[^>]*value="2000"/);

    // total_token_budget defaults to 10000
    expect(html).toMatch(/id="ralph_loop\.limits\.total_token_budget"[^>]*value="10000"/);

    // iteration_timeout_ms defaults to 60000
    expect(html).toMatch(/id="ralph_loop\.limits\.iteration_timeout_ms"[^>]*value="60000"/);

    // total_timeout_ms defaults to 300000
    expect(html).toMatch(/id="ralph_loop\.limits\.total_timeout_ms"[^>]*value="300000"/);

    // abort_patterns defaults to empty (shows "No items")
    expect(html).toContain("Add pattern...");
  });

  it("renders custom config values", () => {
    const config = getDefaultConfig() as IncrediConfig;
    config.ralph_loop = {
      enabled: false,
      build: false,
      tests: false,
      lint: true,
      limits: {
        max_iterations: 5,
        token_budget_per_iteration: 5000,
        total_token_budget: 25000,
        iteration_timeout_ms: 120000,
        total_timeout_ms: 600000,
      },
      abort_patterns: ["FATAL:", "OutOfMemory"],
    };
    const html = getSettingsHtml({ cspSource: "test-csp" } as any, config);

    // enabled is false (not checked)
    const enabledMatch = html.match(/id="ralph_loop\.enabled"[^>]*/);
    expect(enabledMatch).toBeTruthy();
    expect(enabledMatch![0]).not.toContain("checked");

    // build is false (not checked)
    const buildMatch = html.match(/id="ralph_loop\.build"[^>]*/);
    expect(buildMatch).toBeTruthy();
    expect(buildMatch![0]).not.toContain("checked");

    // tests is false (not checked)
    const testsMatch = html.match(/id="ralph_loop\.tests"[^>]*/);
    expect(testsMatch).toBeTruthy();
    expect(testsMatch![0]).not.toContain("checked");

    // lint is true (checked)
    expect(html).toMatch(/id="ralph_loop\.lint"[^>]*checked/);

    // Number inputs reflect custom values
    expect(html).toMatch(/id="ralph_loop\.limits\.max_iterations"[^>]*value="5"/);
    expect(html).toMatch(/id="ralph_loop\.limits\.token_budget_per_iteration"[^>]*value="5000"/);
    expect(html).toMatch(/id="ralph_loop\.limits\.total_token_budget"[^>]*value="25000"/);
    expect(html).toMatch(/id="ralph_loop\.limits\.iteration_timeout_ms"[^>]*value="120000"/);
    expect(html).toMatch(/id="ralph_loop\.limits\.total_timeout_ms"[^>]*value="600000"/);

    // abort_patterns shows custom entries
    expect(html).toContain("FATAL:");
    expect(html).toContain("OutOfMemory");
  });

  it("renders tier badges when showBadges is true", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const sources: ConfigSourceMap = {
      "ralph_loop.enabled": "project",
      "ralph_loop.limits.max_iterations": "local",
    };

    // merged view shows badges
    const html = getSettingsHtml({ cspSource: "test-csp" } as any, config, new Set(), sources, {
      currentTier: "merged",
      defaultEditTier: "project",
      hasGlobalConfig: false,
      hasLocalConfig: false,
      hasProjectConfig: true,
      activeEnvVars: [],
    });

    expect(html).toContain('id="section-ralph_loop"');
    expect(html).toContain('data-path="ralph_loop.enabled"');
    // Badges appear because we're in merged view with sources
    expect(html).toContain("setting-modified");
  });
});
