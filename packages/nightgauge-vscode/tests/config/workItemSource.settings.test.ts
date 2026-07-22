/**
 * Unit tests for workItemSourceSettings.ts
 *
 * Tests ConfigBridge integration, fallback behavior, and default resolution.
 *
 * @see Issue #2571 - Add work item source configuration and provider selection wiring
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { getWorkItemSourceConfig } from "../../src/config/workItemSourceSettings";
import { ConfigBridge } from "../../src/services/ConfigBridge";
import { DEFAULT_CONFIG } from "../../src/config/schema";

// Mock ConfigBridge
vi.mock("../../src/services/ConfigBridge", () => ({
  ConfigBridge: {
    getInstance: vi.fn(),
  },
}));

describe("getWorkItemSourceConfig", () => {
  let mockConfigBridge: {
    isInitialized: ReturnType<typeof vi.fn>;
    getValue: ReturnType<typeof vi.fn>;
  };

  beforeEach(() => {
    mockConfigBridge = {
      isInitialized: vi.fn(),
      getValue: vi.fn(),
    };
    vi.mocked(ConfigBridge.getInstance).mockReturnValue(
      mockConfigBridge as unknown as ConfigBridge
    );
    vi.spyOn(console, "debug").mockImplementation(() => {});
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("returns default mode='github' when ConfigBridge is not initialized", () => {
    mockConfigBridge.isInitialized.mockReturnValue(false);

    const config = getWorkItemSourceConfig();

    expect(config.mode).toBe("github");
  });

  it("logs debug message when ConfigBridge is not initialized", () => {
    mockConfigBridge.isInitialized.mockReturnValue(false);

    getWorkItemSourceConfig();

    expect(console.debug).toHaveBeenCalledWith(
      "[Nightgauge] ConfigBridge not initialized, using defaults for work_item_source"
    );
  });

  it("returns default mode='github' when work_item_source is not in config", () => {
    mockConfigBridge.isInitialized.mockReturnValue(true);
    mockConfigBridge.getValue.mockReturnValue(undefined);

    const config = getWorkItemSourceConfig();

    expect(config.mode).toBe("github");
  });

  it("returns configured mode when specified in config", () => {
    mockConfigBridge.isInitialized.mockReturnValue(true);
    mockConfigBridge.getValue.mockReturnValue({ mode: "repo" });

    const config = getWorkItemSourceConfig();

    expect(config.mode).toBe("repo");
  });

  it("reads from 'work_item_source' path in ConfigBridge", () => {
    mockConfigBridge.isInitialized.mockReturnValue(true);
    mockConfigBridge.getValue.mockReturnValue({ mode: "composite" });

    getWorkItemSourceConfig();

    expect(mockConfigBridge.getValue).toHaveBeenCalledWith("work_item_source");
  });

  it("passes provider_options through unchanged", () => {
    const providerOptions = { url: "https://jira.example.com", project: "PROJ" };
    mockConfigBridge.isInitialized.mockReturnValue(true);
    mockConfigBridge.getValue.mockReturnValue({
      mode: "repo",
      provider_options: providerOptions,
    });

    const config = getWorkItemSourceConfig();

    expect(config.provider_options).toEqual(providerOptions);
  });

  it("returns undefined provider_options when not configured", () => {
    mockConfigBridge.isInitialized.mockReturnValue(true);
    mockConfigBridge.getValue.mockReturnValue({ mode: "github" });

    const config = getWorkItemSourceConfig();

    expect(config.provider_options).toBeUndefined();
  });

  it("matches DEFAULT_CONFIG.work_item_source defaults", () => {
    mockConfigBridge.isInitialized.mockReturnValue(false);

    const config = getWorkItemSourceConfig();
    const schemaDefaults = DEFAULT_CONFIG.work_item_source!;

    expect(config.mode).toBe(schemaDefaults.mode);
  });
});
