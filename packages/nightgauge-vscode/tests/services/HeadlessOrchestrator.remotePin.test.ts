/**
 * HeadlessOrchestrator.remotePin.test.ts
 *
 * A remote run request's pin (#1656): every stage attempt runs the requested
 * model, whatever a caller or a retry path passes; model-swap retries (the
 * Fable → Opus usage-limit fallback, the pr-create sonnet retry) do not run on
 * a pinned run; and a queued pinned item keeps its pin on auto-start.
 */
import { describe, it, expect, beforeEach, vi } from "vitest";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { Logger } from "../../src/utils/logger";

vi.mock("../../src/utils/skillRunner", () => ({
  hasActiveProcess: vi.fn().mockReturnValue(false),
  killAllActiveProcesses: vi.fn(),
  getActiveInteractiveProcess: vi.fn().mockReturnValue(null),
  runStageSkillHeadless: vi.fn(),
  getNextStage: vi.fn(),
  getStageLabel: vi.fn((stage: string) => stage),
  resolveModel: vi.fn().mockReturnValue({ model: "fable", source: "default" }),
}));

const PIN = { adapter: "opencode", model: "lmstudio/qwen/qwen3.8-27b" };

type Internals = {
  requestedPin: { adapter: string; model?: string } | null;
  stageModelOverrides: Map<string, string>;
  shouldFallbackFableToOpus: (stage: string, error: Error | undefined) => boolean;
  pinnedModelSwapRefusal: (swapTo: string, cause: string) => Error | null;
  runStage: (...args: unknown[]) => Promise<unknown>;
  runStageInner: (...args: unknown[]) => Promise<unknown>;
  runPipeline: (...args: unknown[]) => Promise<unknown>;
  startNextQueuedIssue: (item: Record<string, unknown>) => Promise<void>;
};

function makeOrch(): Internals {
  const orch = new HeadlessOrchestrator(
    null as never,
    { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() } as unknown as Logger,
    { contextFileWaitMs: 0 } as never
  );
  vi.spyOn(
    orch as never as { getWorkingDirectory: () => string },
    "getWorkingDirectory"
  ).mockReturnValue("/tmp/ws");
  return orch as unknown as Internals;
}

describe("HeadlessOrchestrator — remote run request pin (#1656)", () => {
  let orch: Internals;

  beforeEach(() => {
    vi.clearAllMocks();
    orch = makeOrch();
  });

  it("runStage runs the requested model whatever the caller passes, as user-override", async () => {
    orch.requestedPin = PIN;
    const inner = vi.spyOn(orch, "runStageInner").mockResolvedValue({ success: true });
    // The pr-create sonnet retry, the Fable → Opus retry and the backoff retry
    // all re-enter runStage; each passes its own model or none.
    await orch.runStage("pr-create", 7, undefined, undefined, "opus", "/tmp/ws");
    await orch.runStage("feature-dev", 7, undefined, undefined, undefined, "/tmp/ws");
    for (const call of inner.mock.calls) {
      expect(call[4]).toBe(PIN.model);
      expect(call[6]).toBe("user-override");
    }
  });

  it("an unpinned runStage passes the caller's model through", async () => {
    const inner = vi.spyOn(orch, "runStageInner").mockResolvedValue({ success: true });
    await orch.runStage("feature-dev", 7, undefined, undefined, "opus", "/tmp/ws");
    expect(inner.mock.calls[0][4]).toBe("opus");
    expect(inner.mock.calls[0][6]).toBeUndefined();
  });

  it("never falls back from Fable to Opus on a pinned run", () => {
    orch.stageModelOverrides.set("feature-dev", "fable");
    const limit = new Error("[rate-limit-quota-exhausted] x");
    expect(orch.shouldFallbackFableToOpus("feature-dev", limit)).toBe(true);
    orch.requestedPin = PIN;
    expect(orch.shouldFallbackFableToOpus("feature-dev", limit)).toBe(false);
  });

  it("refuses a model-swap retry on a pinned run with a reason naming the pin", () => {
    expect(orch.pinnedModelSwapRefusal("sonnet", "PR creation failed")).toBeNull();
    orch.requestedPin = PIN;
    const refusal = orch.pinnedModelSwapRefusal("sonnet", "PR creation failed");
    expect(refusal?.message).toContain("PR creation failed");
    expect(refusal?.message).toContain("not retried on sonnet");
    expect(refusal?.message).toContain(`${PIN.adapter} ${PIN.model}`);
  });

  it("auto-start keeps a queued item's pin", async () => {
    const run = vi.spyOn(orch, "runPipeline").mockResolvedValue({ success: true });
    await orch.startNextQueuedIssue({
      issueNumber: 7,
      title: "t",
      requestedAdapter: PIN.adapter,
      requestedModel: PIN.model,
    });
    expect(run.mock.calls[0][0]).toBe(7);
    expect(run.mock.calls[0][3]).toEqual(PIN);

    await orch.startNextQueuedIssue({ issueNumber: 8, title: "t" });
    expect(run.mock.calls[1][3]).toBeUndefined();
  });
});
