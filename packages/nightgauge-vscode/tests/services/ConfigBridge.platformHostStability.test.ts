/**
 * ConfigBridge.platformHostStability.test.ts
 *
 * The platform host key names the SecretStorage bucket that holds the signed-in
 * user's credentials, and `onPlatformHostChanged` is wired to auth teardown:
 * SessionManager transitions to `unauthenticated` and TokenRefreshManager
 * cancels the refresh scheduler.
 *
 * So a host key that is not a pure function of the effective endpoint is not a
 * cosmetic wart — it is a spurious sign-out. `TokenStorage` is constructed
 * during bootstrap *before* ConfigBridge has loaded anything, so the first
 * reload() compares a host key derived from `undefined` against one derived
 * from the merged config. While those two disagreed for the very same endpoint
 * ("api.nightgauge.dev" vs "production"), every activation fired a host change:
 * the token was written to one bucket and read from the other, the Go daemon
 * was handed an empty credential and fell back to its license key, and the
 * user-scoped analytics routes (Health, Trends, Cost, Compliance) failed with
 * `credential insufficient` until the user signed out and back in.
 *
 * @see Issue #797 - One endpoint, one token key
 * @see Issue #3723 - Reset auth state on platform host change
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.unmock("../../src/services/ConfigBridge");

import { ConfigBridge } from "../../src/services/ConfigBridge";
import { DEFAULT_CONFIG } from "../../src/config/schema";

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

/** The platform section the merge engine yields; overridden per test. */
let platformSection: Record<string, unknown> | undefined;

vi.mock("../../src/views/settings/IncrediYamlService", () => ({
  IncrediYamlService: vi.fn(function () {
    return {
      readEffective: vi.fn(async () => ({
        config: { platform: platformSection },
        sources: {},
        validation: { valid: true, errors: [] },
        envVarsApplied: [],
        cliOverrides: [],
        envVarErrors: [],
        tiers: {
          hasDefaults: true,
          hasGlobal: false,
          hasProject: true,
          hasLocal: false,
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

const createMockWorkspaceManager = () =>
  ({
    getAllRepositories: vi.fn().mockReturnValue([{ name: "test-repo", path: "/test/workspace" }]),
    isMultiWorkspace: vi.fn().mockReturnValue(false),
    getWorkspaceRoot: vi.fn().mockReturnValue("/test/workspace"),
  }) as never;

describe("ConfigBridge — platform host key stability (#797)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    ConfigBridge.resetInstance();
  });

  afterEach(() => {
    ConfigBridge.resetInstance();
  });

  async function hostChangesDuringInitialLoad(
    platform: Record<string, unknown> | undefined
  ): Promise<Array<{ previousHost: string; newHost: string }>> {
    platformSection = platform;
    const bridge = ConfigBridge.getInstance();
    const fired: Array<{ previousHost: string; newHost: string }> = [];
    bridge.onPlatformHostChanged((e) => fired.push(e));
    await bridge.initialize(createMockWorkspaceManager(), "/test/workspace");
    return fired;
  }

  it("does not fire a host change on the initial load of the default config", async () => {
    // DEFAULT_CONFIG is what the merge engine starts from, so this is the
    // ordinary activation path for a user who never touched platform config.
    const fired = await hostChangesDuringInitialLoad(
      DEFAULT_CONFIG.platform as unknown as Record<string, unknown>
    );
    expect(fired).toEqual([]);
  });

  it("does not fire a host change when config omits the platform section entirely", async () => {
    const fired = await hostChangesDuringInitialLoad(undefined);
    expect(fired).toEqual([]);
  });

  it("does not fire when the production endpoint is spelled as an explicit api_url", async () => {
    const fired = await hostChangesDuringInitialLoad({
      enabled: true,
      api_url: "https://api.nightgauge.dev",
    });
    expect(fired).toEqual([]);
  });

  it("still fires when the endpoint genuinely changes", async () => {
    // The guard must not be so broad that a real host switch stops tearing
    // down auth state — that is what #3723 wired the event up for.
    const fired = await hostChangesDuringInitialLoad({ enabled: true, environment: "canary" });
    expect(fired).toEqual([{ previousHost: "production", newHost: "canary" }]);
  });
});
