/**
 * The `nightgauge.platform.url` setting (#1474): precedence, how it feeds the
 * existing resolution path, and ConfigBridge reacting to a change.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.unmock("../../src/services/ConfigBridge");

const settings: { url?: string } = {};
const configListeners: Array<(e: { affectsConfiguration: (s: string) => boolean }) => void> = [];

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
    getConfiguration: vi.fn((section: string) => ({
      get: (key: string, fallback: unknown) =>
        section === "nightgauge.platform" && key === "url" ? (settings.url ?? fallback) : fallback,
    })),
    onDidChangeConfiguration: vi.fn((listener) => {
      configListeners.push(listener);
      return { dispose: vi.fn() };
    }),
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

let platformSection: Record<string, unknown> | undefined;

vi.mock("../../src/views/settings/NightgaugeYamlService", () => ({
  NightgaugeYamlService: vi.fn(function () {
    return {
      readEffective: vi.fn(async () => ({
        config: { platform: platformSection },
        sources: {},
        validation: { valid: true, errors: [] },
        envVarsApplied: [],
        cliOverrides: [],
        envVarErrors: [],
        tiers: {},
        mergeTimeMs: 1,
      })),
      onDidChange: vi.fn(() => ({ dispose: vi.fn() })),
      dispose: vi.fn(),
    };
  }),
}));

import { ConfigBridge } from "../../src/services/ConfigBridge";
import { resolvePlatformBaseUrl } from "../../src/config/schema";
import {
  applyPlatformUrlOverride,
  describePlatformEndpoint,
  readPlatformUrlOverride,
  resolvePlatformUrlOverride,
} from "../../src/platform/platformUrlSetting";

const STAGING = "https://staging.example.test";

describe("resolvePlatformUrlOverride", () => {
  it("prefers the environment variable over the setting", () => {
    expect(resolvePlatformUrlOverride("https://env.example.test", STAGING)).toEqual({
      url: "https://env.example.test",
      source: "NIGHTGAUGE_PLATFORM_URL",
    });
  });

  it("uses the setting when the environment variable is unset or blank", () => {
    expect(resolvePlatformUrlOverride("  ", ` ${STAGING} `)).toEqual({
      url: STAGING,
      source: "nightgauge.platform.url",
    });
  });

  it("is undefined when neither is set", () => {
    expect(resolvePlatformUrlOverride(undefined, "")).toBeUndefined();
  });
});

describe("readPlatformUrlOverride", () => {
  beforeEach(() => {
    settings.url = undefined;
  });

  it("reads the nightgauge.platform.url setting", () => {
    settings.url = STAGING;
    expect(readPlatformUrlOverride({})).toEqual({
      url: STAGING,
      source: "nightgauge.platform.url",
    });
  });

  it("lets NIGHTGAUGE_PLATFORM_URL win", () => {
    settings.url = STAGING;
    expect(
      readPlatformUrlOverride({ NIGHTGAUGE_PLATFORM_URL: "https://env.example.test" })?.source
    ).toBe("NIGHTGAUGE_PLATFORM_URL");
  });
});

describe("applyPlatformUrlOverride", () => {
  it("feeds resolvePlatformBaseUrl as a custom endpoint, over a config preset", () => {
    const cfg = applyPlatformUrlOverride(
      { enabled: true, environment: "canary" },
      { url: STAGING, source: "nightgauge.platform.url" }
    );
    expect(cfg).toEqual({ enabled: true, environment: "custom", api_url: STAGING });
    expect(resolvePlatformBaseUrl(cfg)).toBe(STAGING);
  });

  it("leaves the config alone without an override", () => {
    const cfg = { environment: "canary" as const };
    expect(applyPlatformUrlOverride(cfg, undefined)).toBe(cfg);
  });

  it("keeps HTTPS enforcement for the override", () => {
    const cfg = applyPlatformUrlOverride(undefined, {
      url: "http://staging.example.test",
      source: "nightgauge.platform.url",
    });
    expect(() => resolvePlatformBaseUrl(cfg)).toThrow(/HTTPS/);
    expect(describePlatformEndpoint(cfg)).toMatchObject({ baseUrl: null, nonDefault: true });
  });
});

describe("describePlatformEndpoint", () => {
  it("calls production the default however it is spelled", () => {
    expect(describePlatformEndpoint(undefined).nonDefault).toBe(false);
    expect(
      describePlatformEndpoint({ environment: "custom", api_url: "https://api.nightgauge.dev/" })
        .nonDefault
    ).toBe(false);
  });

  it("marks any other endpoint non-default", () => {
    expect(describePlatformEndpoint({ api_url: STAGING })).toEqual({
      baseUrl: STAGING,
      nonDefault: true,
    });
  });
});

describe("ConfigBridge with the setting", () => {
  const workspaceManager = {
    getAllRepositories: vi.fn().mockReturnValue([{ name: "r", path: "/w" }]),
    isMultiWorkspace: vi.fn().mockReturnValue(false),
    getWorkspaceRoot: vi.fn().mockReturnValue("/w"),
  } as never;
  const savedEnv = process.env.NIGHTGAUGE_PLATFORM_URL;

  beforeEach(() => {
    delete process.env.NIGHTGAUGE_PLATFORM_URL;
    settings.url = undefined;
    platformSection = { enabled: true };
    configListeners.length = 0;
    ConfigBridge.resetInstance();
  });

  afterEach(() => {
    ConfigBridge.resetInstance();
    if (savedEnv === undefined) delete process.env.NIGHTGAUGE_PLATFORM_URL;
    else process.env.NIGHTGAUGE_PLATFORM_URL = savedEnv;
  });

  it("applies the setting to getPlatform() and reports its source", async () => {
    settings.url = STAGING;
    const bridge = ConfigBridge.getInstance();
    await bridge.initialize(workspaceManager, "/w");

    expect(resolvePlatformBaseUrl(bridge.getPlatform())).toBe(STAGING);
    expect(bridge.getPlatformUrlOverride()?.source).toBe("nightgauge.platform.url");
  });

  it("reloads and fires a host change when the setting changes", async () => {
    const bridge = ConfigBridge.getInstance();
    await bridge.initialize(workspaceManager, "/w");
    const fired: Array<{ previousHost: string; newHost: string }> = [];
    bridge.onPlatformHostChanged((e) => fired.push(e));

    settings.url = STAGING;
    for (const l of configListeners) {
      l({ affectsConfiguration: (s) => s === "nightgauge.platform.url" });
    }
    await vi.waitFor(() => expect(fired).toHaveLength(1));

    expect(fired[0]).toEqual({ previousHost: "production", newHost: "staging.example.test" });
    expect(resolvePlatformBaseUrl(bridge.getPlatform())).toBe(STAGING);
  });

  it("ignores changes to other settings", async () => {
    const bridge = ConfigBridge.getInstance();
    await bridge.initialize(workspaceManager, "/w");
    const fired: unknown[] = [];
    bridge.onPlatformHostChanged((e) => fired.push(e));

    settings.url = STAGING;
    for (const l of configListeners) {
      l({ affectsConfiguration: (s) => s === "nightgauge.dashboardUrl" });
    }
    await new Promise((r) => setTimeout(r, 10));

    expect(fired).toEqual([]);
  });
});
