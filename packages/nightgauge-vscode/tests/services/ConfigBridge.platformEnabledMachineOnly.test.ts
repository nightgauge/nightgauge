/**
 * ConfigBridge.platformEnabledMachineOnly.test.ts
 *
 * The `platform` block is the machine's: `platform.enabled` is its opt-in to
 * the hosted service, `platform.api_url` decides where the extension's
 * credentials go, and `platform.telemetry` holds its telemetry switches. A
 * repository tier — the committed `.nightgauge/config.yaml` every clone
 * carries, or the per-checkout local file — must set none of them for
 * whoever opens the repository. The Go loader deletes the block from those
 * tiers (#1049); the merge engine does the same, so getPlatform() reports the
 * machine tier's block whatever the repository says, and a machine-tier
 * `enabled: true` stays on when a repository repeats it.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.unmock("../../src/services/ConfigBridge");

import { ConfigBridge } from "../../src/services/ConfigBridge";
import { mergeConfigs, type ConfigTiers } from "../../src/config/configMergeEngine";

vi.mock("vscode", () => ({
  EventEmitter: class EventEmitter<T> {
    private _listeners: Array<(e: T) => void> = [];
    event = (listener: (e: T) => void) => {
      this._listeners.push(listener);
      return { dispose: () => {} };
    };
    fire = (event: T) => {
      this._listeners.forEach((l) => l(event));
    };
    dispose = vi.fn();
  },
  workspace: {
    createFileSystemWatcher: vi.fn(() => ({
      onDidChange: vi.fn(() => ({ dispose: vi.fn() })),
      onDidCreate: vi.fn(() => ({ dispose: vi.fn() })),
      onDidDelete: vi.fn(() => ({ dispose: vi.fn() })),
      dispose: vi.fn(),
    })),
    fs: { readFile: vi.fn() },
  },
  Uri: { file: (p: string) => ({ fsPath: p }) },
  RelativePattern: class RelativePattern {
    constructor(
      public base: string,
      public pattern: string
    ) {}
  },
}));

/** The tiers the real merge engine combines; set per test. */
let tiers: ConfigTiers = {};

vi.mock("../../src/views/settings/NightgaugeYamlService", () => ({
  NightgaugeYamlService: vi.fn(function () {
    return {
      readEffective: vi.fn(async () =>
        mergeConfigs(tiers, { skipEnvResolution: true, skipValidation: true })
      ),
      onDidChange: vi.fn(() => ({ dispose: vi.fn() })),
      dispose: vi.fn(),
    };
  }),
}));

const workspaceManager = {
  getAllRepositories: vi.fn().mockReturnValue([{ name: "test-repo", path: "/test/workspace" }]),
  isMultiWorkspace: vi.fn().mockReturnValue(false),
  getWorkspaceRoot: vi.fn().mockReturnValue("/test/workspace"),
} as never;

describe("ConfigBridge — the platform block is the machine's to set", () => {
  beforeEach(() => ConfigBridge.resetInstance());
  afterEach(() => ConfigBridge.resetInstance());

  async function platformFor(next: ConfigTiers) {
    tiers = next;
    const bridge = ConfigBridge.getInstance();
    await bridge.initialize(workspaceManager, "/test/workspace");
    return bridge.getPlatform();
  }

  it.each(["project", "local"] as const)("ignores enabled: true from the %s tier", async (tier) => {
    const platform = await platformFor({ [tier]: { platform: { enabled: true } } });
    expect(platform?.enabled ?? false).toBe(false);
  });

  // Before, the check asked which tier set the key last, and a repository
  // repeating the machine's `true` turned cloud features off in the extension
  // while the daemon, which reads the machine tier, stayed on.
  it.each(["project", "local"] as const)(
    "keeps the machine tier's enabled: true when the %s tier repeats it",
    async (tier) => {
      const platform = await platformFor({
        global: { platform: { enabled: true } },
        [tier]: { platform: { enabled: true } },
      });
      expect(platform?.enabled).toBe(true);
    }
  );

  it("keeps the machine tier's enabled: true when a repository says false", async () => {
    const platform = await platformFor({
      global: { platform: { enabled: true } },
      project: { platform: { enabled: false } },
    });
    expect(platform?.enabled).toBe(true);
  });

  it("never takes the platform URL, or the telemetry switches, from a repository", async () => {
    const platform = await platformFor({
      global: {
        platform: { enabled: true, telemetry: { enabled: false, usage_reporting: "minimal" } },
      },
      project: {
        platform: {
          api_url: "https://elsewhere.example.test",
          telemetry: { enabled: true, usage_reporting: "full" },
        },
      },
    });
    // The default production URL, not the repository's.
    expect(platform?.api_url).toBe("https://api.nightgauge.dev");
    expect(platform?.telemetry).toEqual({ enabled: false, usage_reporting: "minimal" });
  });

  it.each(["global", "env", "runtime"] as const)(
    "honours enabled: true from the %s tier",
    async (tier) => {
      const platform = await platformFor({ [tier]: { platform: { enabled: true } } });
      expect(platform?.enabled).toBe(true);
    }
  );
});
