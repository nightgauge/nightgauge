/**
 * Tests for the getQuotaSignalIdleMs() resolver introduced in Issue #3702.
 *
 * quota_signal_idle_ms is the idle budget (ms) allowed after ANY rate-limit
 * signal before the quota fast-fail kills the stage. Unlike stall_idle_ms it
 * always resolves to a concrete value (default 15 min) — there is no "unset →
 * undefined" fall-through, because the skillRunner caps it below stallKillMs so
 * a quota signal can only make a stage fail faster, never slower.
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
import {
  getQuotaSignalIdleMs,
  DEFAULT_QUOTA_SIGNAL_IDLE_MS,
  shouldQuotaFastFail,
} from "../../src/utils/resolvers/monitoringResolver";

beforeEach(() => {
  vi.clearAllMocks();
  delete process.env.NIGHTGAUGE_PIPELINE_QUOTA_SIGNAL_IDLE_MS;
});

afterEach(() => {
  delete process.env.NIGHTGAUGE_PIPELINE_QUOTA_SIGNAL_IDLE_MS;
});

describe("getQuotaSignalIdleMs (#3702)", () => {
  it("defaults to 15 min when not configured", () => {
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      exists: false,
      isLegacy: false,
    });
    expect(getQuotaSignalIdleMs("/test/workspace")).toBe(DEFAULT_QUOTA_SIGNAL_IDLE_MS);
    expect(DEFAULT_QUOTA_SIGNAL_IDLE_MS).toBe(900_000);
  });

  it("reads value from NIGHTGAUGE_PIPELINE_QUOTA_SIGNAL_IDLE_MS env var", () => {
    process.env.NIGHTGAUGE_PIPELINE_QUOTA_SIGNAL_IDLE_MS = "600000";
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      exists: false,
      isLegacy: false,
    });
    expect(getQuotaSignalIdleMs("/test/workspace")).toBe(600000);
  });

  it("ignores invalid env var value and falls back to default", () => {
    process.env.NIGHTGAUGE_PIPELINE_QUOTA_SIGNAL_IDLE_MS = "not-a-number";
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      exists: false,
      isLegacy: false,
    });
    expect(getQuotaSignalIdleMs("/test/workspace")).toBe(DEFAULT_QUOTA_SIGNAL_IDLE_MS);
  });

  it("reads quota_signal_idle_ms from YAML config pipeline: section", () => {
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      exists: true,
      isLegacy: false,
    });
    vi.mocked(fs.readFileSync).mockReturnValue(
      "pipeline:\n  quota_signal_idle_ms: 300000\n" as unknown as Buffer
    );
    expect(getQuotaSignalIdleMs("/test/workspace")).toBe(300000);
  });

  it("env var takes precedence over YAML config", () => {
    process.env.NIGHTGAUGE_PIPELINE_QUOTA_SIGNAL_IDLE_MS = "120000";
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      exists: true,
      isLegacy: false,
    });
    vi.mocked(fs.readFileSync).mockReturnValue(
      "pipeline:\n  quota_signal_idle_ms: 480000\n" as unknown as Buffer
    );
    expect(getQuotaSignalIdleMs("/test/workspace")).toBe(120000);
  });

  it("falls back to default when YAML has no quota_signal_idle_ms key", () => {
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      exists: true,
      isLegacy: false,
    });
    vi.mocked(fs.readFileSync).mockReturnValue(
      "pipeline:\n  stall_kill_multiplier: 4\n" as unknown as Buffer
    );
    expect(getQuotaSignalIdleMs("/test/workspace")).toBe(DEFAULT_QUOTA_SIGNAL_IDLE_MS);
  });

  it("accepts 0 as a valid value", () => {
    process.env.NIGHTGAUGE_PIPELINE_QUOTA_SIGNAL_IDLE_MS = "0";
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      exists: false,
      isLegacy: false,
    });
    expect(getQuotaSignalIdleMs("/test/workspace")).toBe(0);
  });
});

describe("shouldQuotaFastFail (#3702)", () => {
  // Models feature-dev: 80-min normal idle budget, soft-signal budget capped to 15 min.
  const EXHAUSTED_MS = 120_000; // 120s aggressive "limited" gate
  const SOFT_BUDGET_MS = 900_000; // 15 min, already capped below stallKillMs

  it("does NOT fire when no rate-limit signal has been seen, even when idle is huge", () => {
    expect(
      shouldQuotaFastFail({
        quotaExhaustedSignalActive: false,
        anyQuotaSignalSeen: false,
        idleSinceQuotaSignalMs: Number.POSITIVE_INFINITY,
        exhaustedFastFailIdleMs: EXHAUSTED_MS,
        quotaSignalIdleBudgetMs: SOFT_BUDGET_MS,
      })
    ).toBe(false);
  });

  it("fires fast (120s) on a hard 'limited' signal", () => {
    expect(
      shouldQuotaFastFail({
        quotaExhaustedSignalActive: true,
        anyQuotaSignalSeen: true,
        idleSinceQuotaSignalMs: 120_000,
        exhaustedFastFailIdleMs: EXHAUSTED_MS,
        quotaSignalIdleBudgetMs: SOFT_BUDGET_MS,
      })
    ).toBe(true);
  });

  it("does NOT fire on a soft signal while still within the soft budget", () => {
    // The #977 regression case before idle crosses the soft budget: a soft
    // allowed_warning was seen but the process is only ~10 min idle.
    expect(
      shouldQuotaFastFail({
        quotaExhaustedSignalActive: false,
        anyQuotaSignalSeen: true,
        idleSinceQuotaSignalMs: 600_000, // 10 min < 15 min budget
        exhaustedFastFailIdleMs: EXHAUSTED_MS,
        quotaSignalIdleBudgetMs: SOFT_BUDGET_MS,
      })
    ).toBe(false);
  });

  it("fires on a soft signal once idle crosses the soft budget — the #977 fix", () => {
    // #977 idled 81 min after a soft 'allowed_warning'; this now trips at 15 min
    // instead of burning the full 80-min idle budget.
    expect(
      shouldQuotaFastFail({
        quotaExhaustedSignalActive: false,
        anyQuotaSignalSeen: true,
        idleSinceQuotaSignalMs: 900_000, // 15 min == budget
        exhaustedFastFailIdleMs: EXHAUSTED_MS,
        quotaSignalIdleBudgetMs: SOFT_BUDGET_MS,
      })
    ).toBe(true);
  });

  it("does not fire on a soft signal below the soft budget even past the 120s hard threshold", () => {
    // Guards against treating the soft signal as aggressively as 'limited'.
    expect(
      shouldQuotaFastFail({
        quotaExhaustedSignalActive: false,
        anyQuotaSignalSeen: true,
        idleSinceQuotaSignalMs: 200_000, // > 120s but < 15 min
        exhaustedFastFailIdleMs: EXHAUSTED_MS,
        quotaSignalIdleBudgetMs: SOFT_BUDGET_MS,
      })
    ).toBe(false);
  });
});
