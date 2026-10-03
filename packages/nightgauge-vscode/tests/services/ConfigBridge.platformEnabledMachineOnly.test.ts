/**
 * ConfigBridge.platformEnabledMachineOnly.test.ts
 *
 * `platform.enabled` is the machine's opt-in to the hosted service: with it
 * on, the extension restores sessions, registers the machine and uploads run
 * telemetry. A repository tier — the committed `.nightgauge/config.yaml` every
 * clone carries, or the per-checkout local file — must not turn it on for
 * whoever opens the repository. The Go daemon strips the platform block from
 * those tiers (#1049); getPlatform() reports `enabled: false` for them.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.unmock("../../src/services/ConfigBridge");

import { ConfigBridge } from "../../src/services/ConfigBridge";

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

/** The tier the merge engine says platform.enabled came from; set per test. */
let enabledSource: string | undefined;

vi.mock("../../src/views/settings/NightgaugeYamlService", () => ({
  NightgaugeYamlService: vi.fn(function () {
    return {
      readEffective: vi.fn(async () => ({
        config: { platform: { enabled: true, api_url: "https://api.nightgauge.dev" } },
        sources: enabledSource ? { "platform.enabled": enabledSource } : {},
        validation: { valid: true, errors: [] },
        envVarsApplied: [],
        cliOverrides: [],
        envVarErrors: [],
        tiers: {
          hasDefaults: true,
          hasGlobal: true,
          hasProject: true,
          hasLocal: true,
          hasEnv: false,
          hasCli: false,
        },
        mergeTimeMs: 1,
      })),
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

describe("ConfigBridge — platform.enabled is the machine's to set", () => {
  beforeEach(() => ConfigBridge.resetInstance());
  afterEach(() => ConfigBridge.resetInstance());

  async function enabledFrom(source: string | undefined): Promise<boolean | undefined> {
    enabledSource = source;
    const bridge = ConfigBridge.getInstance();
    await bridge.initialize(workspaceManager, "/test/workspace");
    return bridge.getPlatform()?.enabled;
  }

  it.each(["project", "local"])("ignores enabled: true from the %s tier", async (tier) => {
    expect(await enabledFrom(tier)).toBe(false);
  });

  it.each(["global", "env", "runtime"])("honours enabled: true from the %s tier", async (tier) => {
    expect(await enabledFrom(tier)).toBe(true);
  });
});
