/**
 * Tests for the consolidated stage-gate / context-recovery dispatch
 * (Issue #3267).
 *
 * Pre-#3267 the post-stage verification block in HeadlessOrchestrator was a
 * 300-line if/else cascade — one branch per recoverable stage, all running
 * the same `generateDeterministicContext + revalidate` flow. The cascade is now
 * collapsed into a single helper, `attemptContextRecovery`.
 *
 * #498: these tests used to drive a `makeFake()` test double whose body was a
 * hand-copied "mirror of the production attemptContextRecovery body" — so the
 * shipped helper was never executed and any regression in it (a stage dropped
 * from `recoverableStages`, the fallback/revalidate order inverted, the wrong
 * error returned) left the file green. They now drive the REAL private helper
 * on a real HeadlessOrchestrator, with only its collaborator — the
 * ContextAssembler — stubbed. That stub is the genuine seam: both
 * `generateDeterministicContext` and the orchestrator's own
 * `validateStageContextOutput` route through it.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";

vi.mock("vscode", () => ({
  workspace: {
    workspaceFolders: [],
    createFileSystemWatcher: vi.fn(() => ({
      onDidChange: vi.fn(),
      onDidCreate: vi.fn(),
      onDidDelete: vi.fn(),
      dispose: vi.fn(),
    })),
  },
  window: {
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
  commands: { executeCommand: vi.fn(), registerCommand: vi.fn() },
  EventEmitter: class {
    event = vi.fn();
    fire = vi.fn();
    dispose = vi.fn();
  },
  Disposable: { from: vi.fn() },
  Uri: { file: (p: string) => ({ fsPath: p }) },
  ExtensionMode: { Production: 1, Development: 2, Test: 3 },
}));

import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { Logger } from "../../src/utils/logger";

type RecoveryOutcome = { recovered: true } | { recovered: false; error: Error };

interface Calls {
  fallbackCalls: Array<{ stage: string; issueNumber: number }>;
  validateCalls: Array<{ stage: string; issueNumber: number }>;
}

function createMockLogger(): Logger {
  return {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  } as unknown as Logger;
}

/**
 * Build a real orchestrator whose ContextAssembler is a recording stub.
 *
 * The stub answers exactly the two calls the recovery path makes:
 *   - generateDeterministicContext → whether the fallback produced a file
 *   - validateStageContextOutput   → the re-validation verdict
 * Everything else in `attemptContextRecovery` — the recoverable-stage
 * allowlist, the call ordering, and which error is propagated — is the
 * shipped code under test.
 */
function makeOrchestrator(
  tmpDir: string,
  opts: {
    fallbackOutcome: (stage: string) => boolean;
    validateOutcome: (stage: string, callIndex: number) => Error | null;
  }
): {
  attemptContextRecovery: (
    stage: string,
    issueNumber: number,
    originalError: Error
  ) => Promise<RecoveryOutcome>;
  calls: Calls;
} {
  const calls: Calls = { fallbackCalls: [], validateCalls: [] };
  let validateCallIndex = 0;

  const orchestrator = new HeadlessOrchestrator(null, createMockLogger());
  orchestrator.setWorktreeOverride(tmpDir);

  const assemblerStub = {
    setContextFileWaitMs: vi.fn(),
    generateDeterministicContext: vi.fn(async (stage: string, issueNumber: number) => {
      calls.fallbackCalls.push({ stage, issueNumber });
      return { generated: opts.fallbackOutcome(stage) };
    }),
    validateStageContextOutput: vi.fn(async (stage: string, issueNumber: number) => {
      calls.validateCalls.push({ stage, issueNumber });
      return { error: opts.validateOutcome(stage, validateCallIndex++) };
    }),
  };
  (orchestrator as unknown as { contextAssembler: unknown }).contextAssembler = assemblerStub;

  return {
    calls,
    attemptContextRecovery: (stage, issueNumber, originalError) =>
      (
        orchestrator as unknown as {
          attemptContextRecovery: (s: string, n: number, e: Error) => Promise<RecoveryOutcome>;
        }
      ).attemptContextRecovery(stage, issueNumber, originalError),
  };
}

describe("attemptContextRecovery dispatch (Issue #3267)", () => {
  let tmpDir: string;

  beforeEach(() => {
    tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "ng-3267-"));
    fs.mkdirSync(path.join(tmpDir, ".nightgauge", "pipeline"), { recursive: true });
  });

  afterEach(() => {
    fs.rmSync(tmpDir, { recursive: true, force: true });
    vi.clearAllMocks();
  });

  it("dispatches recovery for each of the 5 recoverable stages", async () => {
    const stages = [
      "issue-pickup",
      "feature-planning",
      "feature-dev",
      "feature-validate",
      "pr-create",
    ] as const;
    for (const stage of stages) {
      const orch = makeOrchestrator(tmpDir, {
        fallbackOutcome: () => true,
        validateOutcome: () => null,
      });
      const outcome = await orch.attemptContextRecovery(stage, 42, new Error("missing"));
      expect(outcome.recovered).toBe(true);
      expect(orch.calls.fallbackCalls).toEqual([{ stage, issueNumber: 42 }]);
      expect(orch.calls.validateCalls).toEqual([{ stage, issueNumber: 42 }]);
    }
  });

  it("returns recovered=false with original error when fallback declines", async () => {
    const orch = makeOrchestrator(tmpDir, {
      fallbackOutcome: () => false,
      validateOutcome: () => null,
    });
    const original = new Error("missing context");
    const outcome = await orch.attemptContextRecovery("feature-dev", 7, original);
    expect(outcome.recovered).toBe(false);
    if (!outcome.recovered) {
      expect(outcome.error).toBe(original);
    }
    // The fallback was consulted; revalidation must NOT run after a decline.
    expect(orch.calls.fallbackCalls).toHaveLength(1);
    expect(orch.calls.validateCalls).toHaveLength(0);
  });

  it("returns recovered=false with revalidation error when fallback succeeds but revalidate fails", async () => {
    const revalidateErr = new Error("still invalid after fallback");
    const orch = makeOrchestrator(tmpDir, {
      fallbackOutcome: () => true,
      validateOutcome: () => revalidateErr,
    });
    const outcome = await orch.attemptContextRecovery(
      "feature-planning",
      11,
      new Error("first failure")
    );
    expect(outcome.recovered).toBe(false);
    if (!outcome.recovered) {
      // The REVALIDATION error must win over the original — it is the more
      // recent and more specific description of why the stage is unrecoverable.
      expect(outcome.error).toBe(revalidateErr);
    }
  });

  it("does NOT call fallback for non-recoverable stages (e.g., pr-merge)", async () => {
    const orch = makeOrchestrator(tmpDir, {
      fallbackOutcome: () => true,
      validateOutcome: () => null,
    });
    const original = new Error("post-merge verify failed");
    const outcome = await orch.attemptContextRecovery("pr-merge", 99, original);
    expect(outcome.recovered).toBe(false);
    if (!outcome.recovered) {
      expect(outcome.error).toBe(original);
    }
    expect(orch.calls.fallbackCalls).toHaveLength(0);
  });

  it("does NOT call fallback for unknown stage", async () => {
    const orch = makeOrchestrator(tmpDir, {
      fallbackOutcome: () => true,
      validateOutcome: () => null,
    });
    const outcome = await orch.attemptContextRecovery("not-a-real-stage", 1, new Error("x"));
    expect(outcome.recovered).toBe(false);
    expect(orch.calls.fallbackCalls).toHaveLength(0);
  });
});
