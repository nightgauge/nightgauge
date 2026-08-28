/**
 * Tests for NotifierInstancesSection HTML fragment (#3379).
 *
 * Validates the generated markup for correct structure, XSS safety, and
 * accessibility attributes.
 */

import { describe, it, expect } from "vitest";
import {
  getNotifierInstancesSectionHtml,
  NOTIFIER_PROVIDERS,
  type NotifierInstanceRow,
} from "../../../src/views/notifier/NotifierInstancesSection";

const DISCORD_ROW: NotifierInstanceRow = {
  id: "discord",
  type: "discord",
  channel: "#pipeline-alerts",
  status: "connected",
  lastEventSentAt: "2026-05-15T12:00:00.000Z",
  webhookRedacted: "••••abc12345",
};

const MATTERMOST_ROW: NotifierInstanceRow = {
  id: "mattermost",
  type: "mattermost",
  status: "errored",
  lastError: "HTTP 401 Unauthorized",
};

describe("getNotifierInstancesSectionHtml", () => {
  it("renders table headers", () => {
    const html = getNotifierInstancesSectionHtml([], false);
    expect(html).toContain("<th>ID</th>");
    expect(html).toContain("<th>Type</th>");
    expect(html).toContain("<th>Status</th>");
    expect(html).toContain("<th>Actions</th>");
  });

  it("renders empty-state row when notifiers list is empty", () => {
    const html = getNotifierInstancesSectionHtml([], false);
    expect(html).toContain("No notifier instances configured");
    expect(html).toContain("Add Discord");
    expect(html).toContain("Add Mattermost");
  });

  it("renders Add Discord and Add Mattermost toolbar buttons", () => {
    const html = getNotifierInstancesSectionHtml([], false);
    expect(html).toContain('id="notifier-add-discord-btn"');
    expect(html).toContain('id="notifier-add-mattermost-btn"');
  });

  it("disables toolbar buttons when disabled=true", () => {
    const html = getNotifierInstancesSectionHtml([], true);
    // Both add buttons should carry the disabled attribute
    const discordMatch = html.match(/id="notifier-add-discord-btn"[^>]*>/);
    expect(discordMatch?.[0]).toContain("disabled");
    const mattermostMatch = html.match(/id="notifier-add-mattermost-btn"[^>]*>/);
    expect(mattermostMatch?.[0]).toContain("disabled");
  });

  it("renders one table row per notifier", () => {
    const html = getNotifierInstancesSectionHtml([DISCORD_ROW, MATTERMOST_ROW], false);
    const rowCount = (html.match(/class="notifier-row"/g) ?? []).length;
    expect(rowCount).toBe(2);
  });

  it("renders discord badge for discord type", () => {
    const html = getNotifierInstancesSectionHtml([DISCORD_ROW], false);
    expect(html).toContain("badge-discord");
    expect(html).toContain("Discord");
  });

  it("renders mattermost badge for mattermost type", () => {
    const html = getNotifierInstancesSectionHtml([MATTERMOST_ROW], false);
    expect(html).toContain("badge-mattermost");
    expect(html).toContain("Mattermost");
  });

  it("renders connected status pill", () => {
    const html = getNotifierInstancesSectionHtml([DISCORD_ROW], false);
    expect(html).toContain("status-connected");
    expect(html).toContain("Connected");
  });

  it("renders errored status pill", () => {
    const html = getNotifierInstancesSectionHtml([MATTERMOST_ROW], false);
    expect(html).toContain("status-errored");
    expect(html).toContain("Errored");
  });

  it("includes lastError as title attribute on error status cell", () => {
    const html = getNotifierInstancesSectionHtml([MATTERMOST_ROW], false);
    expect(html).toContain('title="HTTP 401 Unauthorized"');
  });

  it("renders action buttons with data attributes", () => {
    const html = getNotifierInstancesSectionHtml([DISCORD_ROW], false);
    expect(html).toContain('data-notifier-action="test"');
    expect(html).toContain('data-notifier-action="remove"');
    expect(html).toContain('data-instance-id="discord"');
  });

  it("escapes HTML in id to prevent XSS", () => {
    const maliciousRow: NotifierInstanceRow = {
      id: '<script>alert("xss")</script>',
      type: "discord",
      status: "unknown",
    };
    const html = getNotifierInstancesSectionHtml([maliciousRow], false);
    expect(html).not.toContain("<script>alert");
    expect(html).toContain("&lt;script&gt;");
  });

  it("escapes HTML in channel to prevent XSS", () => {
    const row: NotifierInstanceRow = {
      id: "discord",
      type: "discord",
      status: "unknown",
      channel: '<img src=x onerror="alert(1)">',
    };
    const html = getNotifierInstancesSectionHtml([row], false);
    expect(html).not.toContain("<img src=x onerror=");
    expect(html).toContain("&lt;img");
  });

  it("renders channel or em dash when absent", () => {
    const withChannel = getNotifierInstancesSectionHtml([DISCORD_ROW], false);
    expect(withChannel).toContain("#pipeline-alerts");

    const withoutChannel = getNotifierInstancesSectionHtml([MATTERMOST_ROW], false);
    expect(withoutChannel).toContain("—");
  });

  it("formats lastEventSentAt as locale string when present", () => {
    const html = getNotifierInstancesSectionHtml([DISCORD_ROW], false);
    // Should NOT contain raw ISO string — it gets formatted
    expect(html).not.toContain("2026-05-15T12:00:00.000Z");
    // Should contain some formatted date fragment
    expect(html).toMatch(/2026/);
  });

  it("renders Never when lastEventSentAt is absent", () => {
    const html = getNotifierInstancesSectionHtml([MATTERMOST_ROW], false);
    expect(html).toContain("Never");
  });

  it("includes inline <style> block", () => {
    const html = getNotifierInstancesSectionHtml([], false);
    expect(html).toContain("<style>");
  });

  it("includes inline <script> block with vscode.postMessage calls", () => {
    const html = getNotifierInstancesSectionHtml([], false);
    expect(html).toContain("<script>");
    expect(html).toContain("vscode.postMessage");
  });

  // Driven off NOTIFIER_PROVIDERS rather than a hardcoded list, so adding a
  // provider without an Add button or a click handler fails here instead of
  // shipping a button that does nothing (#1073).
  describe("every configured provider is offered", () => {
    const html = getNotifierInstancesSectionHtml([], false);

    for (const provider of NOTIFIER_PROVIDERS) {
      it(`renders an Add button for ${provider}`, () => {
        expect(html).toContain(`id="notifier-add-${provider}-btn"`);
      });
    }

    it("wires every Add button to a postMessage handler", () => {
      // The client script iterates the serialized provider list.
      expect(html).toContain(JSON.stringify(NOTIFIER_PROVIDERS));
      expect(html).toContain("notifier-add-' + provider + '-btn");
    });

    it("names every provider in the empty-state copy", () => {
      for (const provider of NOTIFIER_PROVIDERS) {
        const label = provider.charAt(0).toUpperCase() + provider.slice(1);
        expect(html).toContain(`Add ${label}`);
      }
    });
  });

  it("renders a Slack row with its own provider badge", () => {
    const html = getNotifierInstancesSectionHtml(
      [{ id: "slack", type: "slack", channel: "#pipeline", status: "connected" }],
      false
    );
    expect(html).toContain("badge-slack");
    expect(html).toContain("Slack");
  });
});
