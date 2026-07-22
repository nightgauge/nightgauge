import { describe, it, expect } from "vitest";
import { getSettingsHtml } from "../../../src/views/settings/SettingsHtml";
import { getDefaultConfig } from "../../../src/config/schema";
import type { IncrediConfig } from "../../../src/views/settings/types";
import type { ConfigSourceMap } from "../../../src/config/schema";

describe("SettingsHtml automations section", () => {
  it("renders automations section controls", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const html = getSettingsHtml({ cspSource: "test-csp" } as any, config);

    expect(html).toContain('id="section-automations"');

    // General subsection
    expect(html).toContain('data-path="automations.enabled"');
    expect(html).toContain('data-path="automations.dry_run"');
    expect(html).toContain('data-path="automations.log_file"');

    // Triggers subsection
    expect(html).toContain("Configured Triggers");

    // Labels
    expect(html).toContain("Enabled");
    expect(html).toContain("Dry Run");
    expect(html).toContain("Log File");

    // Subsection titles
    expect(html).toContain("General");
    expect(html).toContain("Triggers");
  });

  it("renders correct default values", () => {
    const config = {} as IncrediConfig;
    const html = getSettingsHtml({ cspSource: "test-csp" } as any, config);

    // enabled defaults to true (checked)
    expect(html).toMatch(/id="automations\.enabled"[^>]*checked/);

    // dry_run defaults to false (not checked)
    const dryRunMatch = html.match(/id="automations\.dry_run"[^>]*/);
    expect(dryRunMatch).toBeTruthy();
    expect(dryRunMatch![0]).not.toContain("checked");

    // log_file defaults to '.nightgauge/automations.log'
    expect(html).toMatch(/id="automations\.log_file"[^>]*value="\.nightgauge\/automations\.log"/);

    // triggers count shows "0 triggers"
    expect(html).toContain("0 triggers configured");
  });

  it("renders custom config values", () => {
    const config = getDefaultConfig() as IncrediConfig;
    config.automations = {
      enabled: false,
      dry_run: true,
      log_file: "/var/log/automations.jsonl",
      triggers: [
        {
          name: "notify-on-review",
          trigger: "status:in-review",
          actions: [{ type: "post_slack", message: "Ready for review" }],
        },
        {
          trigger: "status:done",
          actions: [{ type: "add_label", label: "completed" }],
        },
      ],
    };
    const html = getSettingsHtml({ cspSource: "test-csp" } as any, config);

    // enabled is false (not checked)
    const enabledMatch = html.match(/id="automations\.enabled"[^>]*/);
    expect(enabledMatch).toBeTruthy();
    expect(enabledMatch![0]).not.toContain("checked");

    // dry_run is true (checked)
    expect(html).toMatch(/id="automations\.dry_run"[^>]*checked/);

    // log_file reflects custom value
    expect(html).toMatch(/id="automations\.log_file"[^>]*value="\/var\/log\/automations\.jsonl"/);

    // triggers count shows "2 triggers"
    expect(html).toContain("2 triggers configured");
  });

  it("renders tier badges when showBadges is true", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const sources: ConfigSourceMap = {
      "automations.enabled": "project",
      "automations.log_file": "local",
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

    expect(html).toContain('id="section-automations"');
    expect(html).toContain('data-path="automations.enabled"');
    // Badges appear because we're in merged view with sources
    expect(html).toContain("setting-modified");
  });
});
