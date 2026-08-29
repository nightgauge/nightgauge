/**
 * configureSlackNotifications — token validation and keychain storage (#1073).
 */

import { describe, it, expect, beforeEach, vi } from "vitest";

type InputBoxOpts = {
  password?: boolean;
  value?: string;
  validateInput?: (v: string) => string | null;
};

const state: {
  secrets: Map<string, string>;
  inputs: string[];
  quickPick: string | undefined;
  infoChoice: string | undefined;
  clipboard: string;
  lastInputOpts: InputBoxOpts[];
} = {
  secrets: new Map(),
  inputs: [],
  quickPick: undefined,
  infoChoice: undefined,
  clipboard: "",
  lastInputOpts: [],
};

let registered: (() => Promise<void>) | undefined;

vi.mock("vscode", () => ({
  commands: {
    registerCommand: (_id: string, cb: () => Promise<void>) => {
      registered = cb;
      return { dispose: vi.fn() };
    },
  },
  window: {
    showErrorMessage: vi.fn(),
    showInformationMessage: vi.fn(async () => state.infoChoice),
    showQuickPick: vi.fn(async () => state.quickPick),
    showInputBox: vi.fn(async (opts: InputBoxOpts) => {
      state.lastInputOpts.push(opts);
      return state.inputs.shift();
    }),
    showTextDocument: vi.fn(),
  },
  env: { clipboard: { writeText: vi.fn(async (t: string) => void (state.clipboard = t)) } },
  workspace: { workspaceFolders: [{ uri: { fsPath: "/repo" } }] },
  Uri: { joinPath: (...p: unknown[]) => ({ path: p.join("/") }) },
}));

vi.mock("../../src/services/SecretStorageService", () => ({
  SecretStorageService: {
    getInstance: () => ({
      getSecret: async (k: string) => state.secrets.get(k),
      setSecret: async (k: string, v: string) => void state.secrets.set(k, v),
      deleteSecret: async (k: string) => void state.secrets.delete(k),
    }),
  },
  SECRET_KEYS: { slackBotToken: "slackBotToken" },
  // Re-exported by the module and read transitively via ConfigBridge once the
  // command started reporting real config state (#1115). A partial mock of a
  // module is a promise about its whole surface, not just the parts in use.
  PLATFORM_TOKEN_FIELDS: { accessToken: "accessToken", refreshToken: "refreshToken" },
  PLATFORM_ENV_TOKEN_PREFIX: "nightgauge.platform.",
  MATTERMOST_SIGNING_KEY_PREFIX: "nightgauge.mattermost.signingToken.",
  FORGE_SECRET_PREFIX: "nightgauge-forge-",
  platformTokenKey: (envKey: string, field: string) => `${envKey}.${field}`,
  mattermostSigningKey: (channelId: string) => `mm.${channelId}`,
}));

// The command now reports the notifier's real configured state instead of an
// unconditional "add it to YAML" (#1115); these are its new collaborators.
vi.mock("../../src/services/ConfigBridge", () => ({
  ConfigBridge: {
    getInstance: () => ({
      getEffectiveConfig: () => ({
        config: { notifications: { slack: { enabled: true, channel: "C0123456789" } } },
      }),
    }),
  },
}));
vi.mock("../../src/views/settings/NightgaugeYamlService", () => ({
  NightgaugeYamlService: class {
    writeGlobal = vi.fn(async () => ({ success: true }));
  },
}));
vi.mock("../../src/config/settings", () => ({ getWorkspaceRoot: () => "/tmp/ws" }));

const { registerConfigureSlackNotificationsCommand } =
  await import("../../src/commands/configureSlackNotifications");

const TOKEN = "xoxb-" + "zzTESTTOKENzz";

/** Run the command with a queued set of input-box answers. */
async function run(inputs: string[], opts: { existing?: string; quickPick?: string } = {}) {
  state.secrets = new Map(opts.existing ? [["slackBotToken", opts.existing]] : []);
  state.inputs = [...inputs];
  state.quickPick = opts.quickPick;
  state.infoChoice = undefined;
  state.lastInputOpts = [];
  registerConfigureSlackNotificationsCommand();
  await registered!();
}

/** The token input's validateInput, for asserting rejection messages. */
function tokenValidator(): (v: string) => string | null {
  return state.lastInputOpts[0].validateInput!;
}

describe("configureSlackNotifications", () => {
  beforeEach(() => {
    registered = undefined;
    state.clipboard = "";
  });

  it("stores a valid bot token in the keychain", async () => {
    await run([TOKEN, "C0123456789"]);
    expect(state.secrets.get("slackBotToken")).toBe(TOKEN);
  });

  // A live credential must never be echoed back into a visible input box.
  it("masks the token input and does not pre-fill it from the keychain", async () => {
    await run([TOKEN, "C0123456789"], { existing: TOKEN, quickPick: "Update bot token" });
    expect(state.lastInputOpts[0].password).toBe(true);
    expect(state.lastInputOpts[0].value).toBeUndefined();
  });

  it("removes the token when asked", async () => {
    await run([], { existing: TOKEN, quickPick: "Remove bot token" });
    expect(state.secrets.has("slackBotToken")).toBe(false);
  });

  it("stores nothing when the user cancels the token prompt", async () => {
    await run([]);
    expect(state.secrets.has("slackBotToken")).toBe(false);
  });

  // Cancelling at the channel step must not leave a half-configured state.
  it("stores nothing when the user cancels the channel prompt", async () => {
    await run([TOKEN]);
    expect(state.secrets.has("slackBotToken")).toBe(false);
  });

  describe("token validation", () => {
    beforeEach(async () => {
      await run([TOKEN, "C0123456789"]);
    });

    it("accepts a bot token", () => {
      expect(tokenValidator()(TOKEN)).toBeNull();
    });

    it("rejects an empty value", () => {
      expect(tokenValidator()("  ")).toBeTruthy();
    });

    // Each of these is a realistic paste-mistake, and each would otherwise
    // surface as an opaque invalid_auth at the first pipeline run — so the
    // message has to name the actual mistake, not just say "invalid".
    it("names the mistake for a user token", () => {
      expect(tokenValidator()("xoxp-abc")).toMatch(/user token/i);
    });

    it("names the mistake for an app-level token", () => {
      expect(tokenValidator()("xapp-abc")).toMatch(/app-level token/i);
    });

    it("names the mistake for a webhook URL", () => {
      expect(tokenValidator()("https://hooks.slack.com/services/T0/B0/x")).toMatch(/webhook URL/i);
    });
  });

  describe("channel validation", () => {
    beforeEach(async () => {
      await run([TOKEN, "C0123456789"]);
    });

    const channelValidator = () => state.lastInputOpts[1].validateInput!;

    it("accepts a channel id", () => {
      expect(channelValidator()("C0123456789")).toBeNull();
    });

    it("accepts a #channel-name", () => {
      expect(channelValidator()("#pipeline-status")).toBeNull();
    });

    it("rejects a bare name with no # and no id shape", () => {
      expect(channelValidator()("pipeline")).toBeTruthy();
    });

    it("rejects an empty value", () => {
      expect(channelValidator()("")).toBeTruthy();
    });
  });

  it("stores the token and never leaks it to the clipboard (#1115)", async () => {
    // Replaces "copies a config block carrying the chosen channel". The command
    // no longer hands the operator YAML to paste: it prompted for the channel,
    // validated it, and then discarded it, telling them to type it again into a
    // file at the wrong config tier. The channel is now persisted directly, so
    // there is no config block and nothing is copied.
    state.infoChoice = undefined;
    state.secrets = new Map();
    state.inputs = [TOKEN, "C0123456789"];
    state.quickPick = undefined;
    state.lastInputOpts = [];

    registerConfigureSlackNotificationsCommand();
    await registered!();

    // The credential goes to SecretStorage, and only there.
    expect(state.secrets.get("slackBotToken")).toBe(TOKEN);

    // The property the old test protected, kept: a live token must never reach
    // the clipboard. Now trivially true — nothing is written to it at all.
    expect(state.clipboard ?? "").not.toContain(TOKEN);
  });
});
