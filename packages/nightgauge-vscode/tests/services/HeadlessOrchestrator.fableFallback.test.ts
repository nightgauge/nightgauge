/**
 * HeadlessOrchestrator.fableFallback.test.ts
 *
 * Tests the Fable → Opus graceful-fallback decision logic on a usage/quota
 * limit. Fable has a separate Max-plan usage bucket from Opus/Sonnet, so a
 * Fable-only exhaustion should downgrade the stage to Opus and retry rather
 * than pausing the whole pipeline for the global cooldown.
 *
 *   - isUsageLimitError() recognizes the quota marker and session/usage-limit text.
 *   - shouldFallbackFableToOpus() fires only for a Fable stage on a usage limit,
 *     and only once per stage per run.
 */
import { describe, it, expect, beforeEach, vi } from "vitest";
import { HeadlessOrchestrator, isUsageLimitError } from "../../src/services/HeadlessOrchestrator";
import type { Logger } from "../../src/utils/logger";

// Mock skillRunner so importing the orchestrator doesn't pull the real CLI, and
// resolveModel (consulted only when a stage has no override) returns a non-Fable
// default.
vi.mock("../../src/utils/skillRunner", () => ({
  hasActiveProcess: vi.fn().mockReturnValue(false),
  killAllActiveProcesses: vi.fn(),
  getActiveInteractiveProcess: vi.fn().mockReturnValue(null),
  runStageSkillHeadless: vi.fn(),
  getNextStage: vi.fn(),
  getStageLabel: vi.fn((stage: string) => stage),
  resolveModel: vi.fn().mockReturnValue({ model: "sonnet", source: "default" }),
}));

function makeLogger(): Logger {
  return {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  } as unknown as Logger;
}

function makeOrch() {
  const orch = new HeadlessOrchestrator(null as never, makeLogger(), {
    contextFileWaitMs: 0,
  } as never);
  vi.spyOn(
    orch as never as { getWorkingDirectory: () => string },
    "getWorkingDirectory"
  ).mockReturnValue("/tmp/ws");
  return orch as unknown as {
    stageModelOverrides: Map<string, string>;
    fableQuotaFallbackApplied: Set<string>;
    shouldFallbackFableToOpus: (stage: string, error: Error | undefined) => boolean;
  };
}

describe("isUsageLimitError", () => {
  it("matches the canonical quota-exhausted marker", () => {
    expect(isUsageLimitError("[rate-limit-quota-exhausted] resets 10:30am; resetsAt=123")).toBe(
      true
    );
    expect(isUsageLimitError("something rate-limit-quota-exhausted happened")).toBe(true);
  });

  it("matches plain session/usage-limit phrasings", () => {
    expect(
      isUsageLimitError("You've hit your session limit · resets 10:30am (America/Denver)")
    ).toBe(true);
    expect(isUsageLimitError("usage limit reached")).toBe(true);
    expect(isUsageLimitError("You have reached your weekly limit")).toBe(true);
  });

  it("does not match unrelated errors or empty input", () => {
    expect(isUsageLimitError(undefined)).toBe(false);
    expect(isUsageLimitError("")).toBe(false);
    expect(isUsageLimitError("build failed: tsc error TS2345")).toBe(false);
    expect(isUsageLimitError("network timeout")).toBe(false);
  });
});

describe("HeadlessOrchestrator.shouldFallbackFableToOpus", () => {
  let orch: ReturnType<typeof makeOrch>;

  beforeEach(() => {
    vi.clearAllMocks();
    orch = makeOrch();
  });

  it("fires for a Fable stage on a usage limit", () => {
    orch.stageModelOverrides.set("feature-dev", "fable");
    expect(
      orch.shouldFallbackFableToOpus("feature-dev", new Error("[rate-limit-quota-exhausted] x"))
    ).toBe(true);
  });

  it("does NOT fire for a non-usage error", () => {
    orch.stageModelOverrides.set("feature-dev", "fable");
    expect(orch.shouldFallbackFableToOpus("feature-dev", new Error("build failed"))).toBe(false);
  });

  it("does NOT fire when the stage is not on Fable", () => {
    orch.stageModelOverrides.set("feature-validate", "opus");
    expect(
      orch.shouldFallbackFableToOpus("feature-validate", new Error("You've hit your session limit"))
    ).toBe(false);
  });

  it("does NOT fire when the router default (non-Fable) applies and there is no override", () => {
    // No override → resolveModel mock returns "sonnet".
    expect(orch.shouldFallbackFableToOpus("feature-dev", new Error("usage limit reached"))).toBe(
      false
    );
  });

  it("fires only once per stage per run (guard)", () => {
    orch.stageModelOverrides.set("feature-dev", "fable");
    expect(orch.shouldFallbackFableToOpus("feature-dev", new Error("session limit"))).toBe(true);
    orch.fableQuotaFallbackApplied.add("feature-dev"); // simulate the applied guard
    expect(orch.shouldFallbackFableToOpus("feature-dev", new Error("session limit"))).toBe(false);
  });
});
