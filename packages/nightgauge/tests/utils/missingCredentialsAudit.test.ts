import { mkdirSync, mkdtempSync } from "fs";
import { tmpdir } from "os";
import { join } from "path";
import { describe, expect, it, vi } from "vitest";
import { SECRET_KEYS } from "../../src/services/SecretStorageService";
import {
  auditMissingCredentials,
  findMissingCredentials,
  legacyExtensionDataPresent,
  type CredentialProbe,
} from "../../src/utils/missingCredentialsAudit";
import {
  resetMissingCredentialWarnings,
  warnMissingCredentialOnce,
} from "../../src/services/notifications/credentials";

function probe(secrets: Record<string, string> = {}, opts: Partial<CredentialProbe> = {}) {
  return {
    getSecret: async (k: string) => secrets[k],
    getAccessToken: async () => null,
    env: {},
    ...opts,
  } satisfies CredentialProbe;
}

const slackOn = { notifications: { slack: { enabled: true } } };

describe("auditMissingCredentials (#2513)", () => {
  it("Slack enabled + no token → exactly one card naming slack-bot-token", async () => {
    const raise = vi.fn().mockResolvedValue({});
    await auditMissingCredentials({
      config: slackOn,
      probe: probe(),
      repo: "o/r",
      globalStoragePath: undefined,
      raise,
    });
    expect(raise).toHaveBeenCalledTimes(1);
    expect(raise).toHaveBeenCalledWith("o/r", ["slack-bot-token"], false);
  });

  it("Slack disabled → no card", async () => {
    const raise = vi.fn();
    await auditMissingCredentials({
      config: { notifications: { slack: { enabled: false } } },
      probe: probe(),
      repo: "o/r",
      globalStoragePath: undefined,
      raise,
    });
    expect(raise).not.toHaveBeenCalled();
  });

  it("a stored token or env var counts as present", async () => {
    expect(
      await findMissingCredentials(slackOn, probe({ [SECRET_KEYS.slackBotToken]: "xoxb-1" }))
    ).toEqual([]);
    expect(
      await findMissingCredentials(slackOn, probe({}, { env: { SLACK_BOT_TOKEN: "xoxb-1" } }))
    ).toEqual([]);
  });

  it("several missing → one raise with every id, sorted", async () => {
    const raise = vi.fn().mockResolvedValue({});
    await auditMissingCredentials({
      config: {
        notifications: {
          slack: { enabled: true },
          discord: { enabled: true },
          mattermost: { enabled: true },
        },
        platform: { enabled: true },
      },
      probe: probe(),
      repo: "o/r",
      globalStoragePath: undefined,
      raise,
    });
    expect(raise).toHaveBeenCalledTimes(1);
    expect(raise.mock.calls[0][1]).toEqual([
      "discord-webhook",
      "mattermost-webhook",
      "platform-license",
      "slack-bot-token",
    ]);
  });

  it("platform: a license key or an access token is enough", async () => {
    const cfg = { platform: { enabled: true } };
    expect(
      await findMissingCredentials(cfg, probe({ [SECRET_KEYS.platformLicenseKey]: "lk" }))
    ).toEqual([]);
    expect(
      await findMissingCredentials(cfg, probe({}, { getAccessToken: async () => "jwt" }))
    ).toEqual([]);
  });

  it("a raise rejection does not throw", async () => {
    const logger = { warn: vi.fn() };
    await expect(
      auditMissingCredentials({
        config: slackOn,
        probe: probe(),
        repo: "o/r",
        globalStoragePath: undefined,
        raise: vi.fn().mockRejectedValue(new Error("daemon down")),
        logger,
      })
    ).resolves.toEqual([]);
    expect(logger.warn).toHaveBeenCalledTimes(1);
  });

  it("detects the retired extension's global storage beside ours", () => {
    const root = mkdtempSync(join(tmpdir(), "ng-2513-"));
    const ours = join(root, "nightgauge.nightgauge");
    expect(legacyExtensionDataPresent(ours)).toBe(false);
    mkdirSync(join(root, "nightgauge.nightgauge-vscode"));
    expect(legacyExtensionDataPresent(ours)).toBe(true);
  });
});

describe("warnMissingCredentialOnce (#2513)", () => {
  it("warns once per notifier per session", () => {
    resetMissingCredentialWarnings();
    const logger = { warn: vi.fn() };
    warnMissingCredentialOnce(logger, "slack", "no token");
    warnMissingCredentialOnce(logger, "slack", "no token");
    warnMissingCredentialOnce(logger, "discord", "no url");
    expect(logger.warn).toHaveBeenCalledTimes(2);
  });
});
