import { describe, it, expect } from "vitest";
import { getSettingsHtml } from "../../../src/views/settings/SettingsHtml";
import { getDefaultConfig } from "../../../src/config/schema";
import type { IncrediConfig } from "../../../src/views/settings/types";
import type { ConfigSourceMap } from "../../../src/config/schema";

describe("SettingsHtml human_in_the_loop section", () => {
  it("renders human_in_the_loop section controls", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const html = getSettingsHtml({ cspSource: "test-csp" } as any, config);

    expect(html).toContain('id="section-human_in_the_loop"');

    // Auto-Accept subsection
    expect(html).toContain('data-path="human_in_the_loop.auto_accept_stages"');
    expect(html).toContain('data-path="human_in_the_loop.auto_accept_permissions"');

    // Trusted Stages subsection - checkbox group
    expect(html).toContain('data-path="human_in_the_loop.trusted_stages"');
    expect(html).toContain('data-value="issue-pickup"');
    expect(html).toContain('data-value="feature-planning"');
    expect(html).toContain('data-value="feature-dev"');
    expect(html).toContain('data-value="feature-validate"');
    expect(html).toContain('data-value="pr-create"');
    expect(html).toContain('data-value="pr-merge"');

    // Labels
    expect(html).toContain("Auto-Accept Stages");
    expect(html).toContain("Auto-Accept Permissions");
    expect(html).toContain("Trusted Stages");

    // Stage labels
    expect(html).toContain("Issue Pickup");
    expect(html).toContain("Feature Planning");
    expect(html).toContain("Feature Dev");
    expect(html).toContain("Feature Validate");
    expect(html).toContain("PR Create");
    expect(html).toContain("PR Merge");

    // Subsection titles
    expect(html).toContain("Auto-Accept");
  });

  it("renders correct default values", () => {
    const config = {} as IncrediConfig;
    const html = getSettingsHtml({ cspSource: "test-csp" } as any, config);

    // auto_accept_stages defaults to true (checked)
    const stagesMatch = html.match(/id="human_in_the_loop\.auto_accept_stages"[^>]*/);
    expect(stagesMatch).toBeTruthy();
    expect(stagesMatch![0]).toContain("checked");

    // auto_accept_permissions defaults to false (not checked)
    const permsMatch = html.match(/id="human_in_the_loop\.auto_accept_permissions"[^>]*/);
    expect(permsMatch).toBeTruthy();
    expect(permsMatch![0]).not.toContain("checked");

    // All trusted stages checkboxes should be unchecked by default
    const checkboxMatches = html.match(
      /class="checkbox-group-input"[^>]*data-path="human_in_the_loop\.trusted_stages"[^>]*/g
    );
    expect(checkboxMatches).toBeTruthy();
    expect(checkboxMatches!.length).toBe(6);
    for (const match of checkboxMatches!) {
      expect(match).not.toContain("checked");
    }
  });

  it("renders custom config values", () => {
    const config = getDefaultConfig() as IncrediConfig;
    config.human_in_the_loop = {
      auto_accept_stages: true,
      auto_accept_permissions: true,
      trusted_stages: ["feature-planning", "pr-merge"],
    };
    const html = getSettingsHtml({ cspSource: "test-csp" } as any, config);

    // auto_accept_stages is true (checked)
    expect(html).toMatch(/id="human_in_the_loop\.auto_accept_stages"[^>]*checked/);

    // auto_accept_permissions is true (checked)
    expect(html).toMatch(/id="human_in_the_loop\.auto_accept_permissions"[^>]*checked/);

    // feature-planning checkbox should be checked
    expect(html).toMatch(
      /data-path="human_in_the_loop\.trusted_stages"\s+data-value="feature-planning"\s+checked/
    );

    // pr-merge checkbox should be checked
    expect(html).toMatch(
      /data-path="human_in_the_loop\.trusted_stages"\s+data-value="pr-merge"\s+checked/
    );

    // issue-pickup checkbox should NOT be checked
    const issuePickupMatch = html.match(
      /data-path="human_in_the_loop\.trusted_stages"\s+data-value="issue-pickup"[^>]*/
    );
    expect(issuePickupMatch).toBeTruthy();
    expect(issuePickupMatch![0]).not.toContain("checked");
  });

  it("renders tier badges when showBadges is true", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const sources: ConfigSourceMap = {
      "human_in_the_loop.auto_accept_stages": "project",
      "human_in_the_loop.trusted_stages": "local",
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

    expect(html).toContain('id="section-human_in_the_loop"');
    expect(html).toContain('data-path="human_in_the_loop.auto_accept_stages"');
    // Badges appear because we're in merged view with sources
    expect(html).toContain("setting-modified");
  });

  it("handles empty/undefined config gracefully", () => {
    const config = { human_in_the_loop: {} } as IncrediConfig;
    const html = getSettingsHtml({ cspSource: "test-csp" } as any, config);

    // Should render without errors
    expect(html).toContain('id="section-human_in_the_loop"');
    expect(html).toContain('data-path="human_in_the_loop.auto_accept_stages"');
    expect(html).toContain('data-path="human_in_the_loop.auto_accept_permissions"');

    // auto_accept_stages toggle should render as checked (true default)
    const stagesMatch = html.match(/id="human_in_the_loop\.auto_accept_stages"[^>]*/);
    expect(stagesMatch).toBeTruthy();
    expect(stagesMatch![0]).toContain("checked");

    // All 6 stage checkboxes should render unchecked
    const checkboxMatches = html.match(
      /class="checkbox-group-input"[^>]*data-path="human_in_the_loop\.trusted_stages"[^>]*/g
    );
    expect(checkboxMatches).toBeTruthy();
    expect(checkboxMatches!.length).toBe(6);
  });
});
