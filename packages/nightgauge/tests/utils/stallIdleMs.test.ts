/**
 * Tests for the getStallIdleMs() resolver introduced in Issue #3484.
 *
 * stall_idle_ms is an absolute idle-kill threshold (ms) that, when set,
 * replaces the computed `threshold × multiplier` value in skillRunner.ts.
 * When unset, existing behavior is fully preserved.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import * as fs from "node:fs";

vi.mock("vscode", () => ({
  workspace: {
    workspaceFolders: [{ uri: { fsPath: "/test/workspace" } }],
  },
}));

vi.mock("node:fs", () => ({
  existsSync: vi.fn(),
  readFileSync: vi.fn(),
}));

vi.mock("../../src/utils/configPathResolver", () => ({
  resolveConfigPathSync: vi.fn(),
  logDeprecationWarning: vi.fn(),
}));

import { resolveConfigPathSync } from "../../src/utils/configPathResolver";
import { getStallIdleMs } from "../../src/utils/resolvers/monitoringResolver";

/**
 * `fs.readFileSync` is overloaded: with an encoding it returns a string,
 * without one a Buffer. `vi.mocked` resolves to the Buffer overload, so these
 * sites cast a string through `as unknown as Buffer` — claiming the code under
 * test reads bytes when it reads text. It calls `readFileSync(path, "utf-8")`;
 * narrowing the mock to that overload says so (#499).
 */
const readFileSyncUtf8 = vi.mocked(fs.readFileSync as (path: string, encoding: string) => string);

beforeEach(() => {
  vi.clearAllMocks();
  delete process.env.NIGHTGAUGE_PIPELINE_STALL_IDLE_MS;
});

afterEach(() => {
  delete process.env.NIGHTGAUGE_PIPELINE_STALL_IDLE_MS;
});

describe("getStallIdleMs (#3484)", () => {
  it("returns undefined when not configured — preserves existing multiplier behavior", () => {
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      exists: false,
      isLegacy: false,
    });
    expect(getStallIdleMs("/test/workspace")).toBeUndefined();
  });

  it("reads value from NIGHTGAUGE_PIPELINE_STALL_IDLE_MS env var", () => {
    process.env.NIGHTGAUGE_PIPELINE_STALL_IDLE_MS = "480000";
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      exists: false,
      isLegacy: false,
    });
    expect(getStallIdleMs("/test/workspace")).toBe(480000);
  });

  it("ignores invalid env var value and returns undefined", () => {
    process.env.NIGHTGAUGE_PIPELINE_STALL_IDLE_MS = "not-a-number";
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      exists: false,
      isLegacy: false,
    });
    expect(getStallIdleMs("/test/workspace")).toBeUndefined();
  });

  it("reads stall_idle_ms from YAML config pipeline: section", () => {
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      exists: true,
      isLegacy: false,
    });
    readFileSyncUtf8.mockReturnValue("pipeline:\n  stall_idle_ms: 300000\n");
    expect(getStallIdleMs("/test/workspace")).toBe(300000);
  });

  it("env var takes precedence over YAML config", () => {
    process.env.NIGHTGAUGE_PIPELINE_STALL_IDLE_MS = "120000";
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      exists: true,
      isLegacy: false,
    });
    readFileSyncUtf8.mockReturnValue("pipeline:\n  stall_idle_ms: 480000\n");
    expect(getStallIdleMs("/test/workspace")).toBe(120000);
  });

  it("returns undefined when YAML has no stall_idle_ms key", () => {
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      exists: true,
      isLegacy: false,
    });
    readFileSyncUtf8.mockReturnValue("pipeline:\n  stall_kill_multiplier: 4\n");
    expect(getStallIdleMs("/test/workspace")).toBeUndefined();
  });

  it("accepts 0 as a valid value (disables idle kill)", () => {
    process.env.NIGHTGAUGE_PIPELINE_STALL_IDLE_MS = "0";
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      exists: false,
      isLegacy: false,
    });
    expect(getStallIdleMs("/test/workspace")).toBe(0);
  });

  it("returns undefined when no workspaceRoot and no env var", () => {
    expect(getStallIdleMs(undefined)).toBeUndefined();
  });
});
