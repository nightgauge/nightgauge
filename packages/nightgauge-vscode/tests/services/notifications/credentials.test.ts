/**
 * Tests for notifier credential resolution (#1106, #1107).
 */

import { describe, it, expect, vi } from "vitest";
import {
  CREDENTIAL_ENV_VAR,
  isPastedSecret,
  warnOnLegacyEnvKey,
} from "../../../src/services/notifications/credentials";

const fakeLogger = () => ({ warn: vi.fn(), info: vi.fn(), debug: vi.fn(), error: vi.fn() });

/**
 * Obviously-fake bot token, assembled at runtime so no literal in this file
 * matches a credential-scanner pattern. Matching the convention already used
 * in SlackService.test.ts.
 */
const TOKEN_TAIL = "zzTESTTOKENzz";
const FAKE_BOT_TOKEN = "xoxb-" + TOKEN_TAIL;

describe("notifier credentials (#1107)", () => {
  it("pins each notifier's CI fallback to a fixed, documented variable", () => {
    // The name is a constant precisely so it cannot be a settings box that
    // collects secrets. These are the names the docs have always claimed.
    expect(CREDENTIAL_ENV_VAR).toEqual({
      slack: "SLACK_BOT_TOKEN",
      discord: "DISCORD_WEBHOOK_URL",
      mattermost: "MATTERMOST_WEBHOOK_URL",
    });
  });

  describe("isPastedSecret", () => {
    it("accepts real environment variable names", () => {
      for (const name of ["SLACK_BOT_TOKEN", "_PRIVATE", "a1", "DISCORD_WEBHOOK_URL"]) {
        expect(isPastedSecret(name)).toBe(false);
      }
    });

    it("flags a value that cannot possibly name a variable", () => {
      // The observed mistake: a live bot token in the env-var-NAME field.
      // Assembled from parts, and with no digits borrowed from any real
      // workspace, so nothing here can look like a credential to a scanner.
      expect(isPastedSecret(FAKE_BOT_TOKEN)).toBe(true);
      // And the webhook-shaped equivalents for the other two notifiers.
      expect(isPastedSecret("https://hooks.slack.com/services/T000/B000/xxxx")).toBe(true);
      expect(isPastedSecret("https://discord.com/api/webhooks/123/abc")).toBe(true);
    });

    it("does not flag an absent or blank value", () => {
      expect(isPastedSecret("")).toBe(false);
      expect(isPastedSecret("   ")).toBe(false);
    });
  });

  describe("warnOnLegacyEnvKey", () => {
    it("refuses a pasted secret and leads with rotation", () => {
      const logger = fakeLogger();
      const had = warnOnLegacyEnvKey(
        logger as never,
        "slack",
        { bot_token_env: FAKE_BOT_TOKEN },
        "Nightgauge: Configure Slack Notifications"
      );

      expect(had).toBe(true);
      const msg = logger.warn.mock.calls[0][0] as string;
      // By the time this fires the secret is already in a plaintext file, so
      // rotation is the instruction that matters, not reconfiguration.
      expect(msg).toContain("Rotate");
      expect(msg).toContain("plaintext");
      expect(msg).toContain("SLACK_BOT_TOKEN");
      expect(msg).toContain("Nightgauge: Configure Slack Notifications");
    });

    it("never echoes the secret it is refusing", () => {
      const logger = fakeLogger();
      const secret = FAKE_BOT_TOKEN;
      warnOnLegacyEnvKey(logger as never, "slack", { bot_token_env: secret }, "cmd");
      expect(logger.warn.mock.calls[0][0]).not.toContain(secret);
    });

    it("reports a legacy variable NAME as unsupported without a rotation scare", () => {
      const logger = fakeLogger();
      const had = warnOnLegacyEnvKey(
        logger as never,
        "discord",
        { webhook_env: "MY_OWN_WEBHOOK_VAR" },
        "cmd"
      );

      expect(had).toBe(true);
      const msg = logger.warn.mock.calls[0][0] as string;
      expect(msg).toContain("no longer supported");
      expect(msg).toContain("DISCORD_WEBHOOK_URL");
      expect(msg).not.toContain("Rotate");
    });

    it("says nothing when no legacy key is present", () => {
      const logger = fakeLogger();
      expect(warnOnLegacyEnvKey(logger as never, "mattermost", { enabled: true }, "cmd")).toBe(
        false
      );
      expect(warnOnLegacyEnvKey(logger as never, "mattermost", null, "cmd")).toBe(false);
      expect(logger.warn).not.toHaveBeenCalled();
    });
  });
});
