import { describe, it, expect } from "vitest";
import { getSettingsHtml, NOTIFICATION_PROVIDERS } from "../../../src/views/settings/SettingsHtml";
import { getDefaultConfig, NotificationsConfigSchema } from "../../../src/config/schema";
import type { NightgaugeConfig } from "../../../src/views/settings/types";

const webview = { cspSource: "test-csp" } as never;

describe("SettingsHtml notifications section", () => {
  it("renders the section", () => {
    const html = getSettingsHtml(webview, getDefaultConfig() as NightgaugeConfig);
    expect(html).toContain('id="section-notifications"');
  });

  // The regression test for #1096. Before it, `notifications.*` had no GUI at
  // all: the Notifier Settings panel handles credentials but never writes
  // config, so `enabled` and `channel` were hand-edited YAML with nothing in
  // the settings UI to suggest the feature existed.
  //
  // Pinning the rendered providers against the *schema* rather than against a
  // literal list is what makes this durable. Adding a fourth provider to
  // NotificationsConfigSchema and forgetting the settings surface reproduces
  // exactly the #1096 defect, and fails here instead of shipping silently.
  it("renders every provider the notifications schema declares", () => {
    const schemaProviders = Object.keys(NotificationsConfigSchema.shape).sort();
    const renderedProviders = NOTIFICATION_PROVIDERS.map((p) => p.id).sort();

    expect(renderedProviders).toEqual(schemaProviders);
  });

  it("gives every provider an enabled toggle — no provider is reachable by a route the others lack", () => {
    const html = getSettingsHtml(webview, getDefaultConfig() as NightgaugeConfig);

    for (const provider of NOTIFICATION_PROVIDERS) {
      expect(html, `${provider.id} has no enabled toggle`).toContain(
        `data-path="notifications.${provider.id}.enabled"`
      );
      expect(html, `${provider.id} is not labelled`).toContain(provider.label);
    }
  });

  it("renders the Slack channel field", () => {
    const html = getSettingsHtml(webview, getDefaultConfig() as NightgaugeConfig);

    // Slack stays silent when enabled with no channel, and that failure is
    // invisible — it was the exact state a user was stuck in during #1070.
    expect(html).toContain('data-path="notifications.slack.channel"');
  });

  it("reflects configured values", () => {
    const config = {
      notifications: {
        slack: { enabled: true, channel: "C0123456789" },
        discord: { enabled: false },
      },
    } as NightgaugeConfig;
    const html = getSettingsHtml(webview, config);

    expect(html).toMatch(/id="notifications\.slack\.enabled"[^>]*checked/);
    expect(html).toMatch(/id="notifications\.slack\.channel"[^>]*value="C0123456789"/);

    const discord = html.match(/id="notifications\.discord\.enabled"[^>]*/);
    expect(discord).toBeTruthy();
    expect(discord![0]).not.toContain("checked");
  });

  it("routes credentials to the Notifier Settings panel instead of a config field", () => {
    const html = getSettingsHtml(webview, getDefaultConfig() as NightgaugeConfig);

    expect(html).toContain("open-notifier-settings");

    // Tokens and webhook URLs belong in SecretStorage. The section names the
    // env var that holds a credential; it must never offer the credential
    // itself as a config-tier field.
    expect(html).not.toContain('data-path="notifications.slack.bot_token"');
    expect(html).not.toContain('data-path="notifications.discord.webhook_url"');
    expect(html).not.toContain('data-path="notifications.mattermost.webhook_url"');
  });
});
