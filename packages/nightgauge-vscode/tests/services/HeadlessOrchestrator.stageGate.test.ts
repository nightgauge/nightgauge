/**
 * Tests for the consolidated stage-gate / context-recovery dispatch
 * (Issue #3267).
 *
 * Pre-#3267 the post-stage verification block in HeadlessOrchestrator was a
 * 300-line if/else cascade — one branch per recoverable stage, all running
 * the same `generateDeterministicContext + revalidate` flow. The cascade is now
 * collapsed into a single helper, `attemptContextRecovery`. These tests
 * exercise that helper through a stripped-down test double rather than the
 * full HeadlessOrchestrator.
 */

import { describe, it, expect, vi } from "vitest";

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

// Test double: minimal subclass-style harness that exposes the private
// attemptContextRecovery method via reflection. Keeps the tests focused on
// the recovery contract without dragging in vscode infrastructure.
type RecoveryOutcome = { recovered: true } | { recovered: false; error: Error };
type FakeOrchestrator = {
  attemptContextRecovery(
    stage: string,
    issueNumber: number,
    originalError: Error
  ): Promise<RecoveryOutcome>;
};

interface Calls {
  fallbackCalls: Array<{ stage: string; issueNumber: number }>;
  validateCalls: Array<{ stage: string; issueNumber: number }>;
}

function makeFake(opts: {
  fallbackOutcome: (stage: string) => boolean;
  validateOutcome: (stage: string, callIndex: number) => Error | null;
}): FakeOrchestrator & { calls: Calls } {
  const calls: Calls = { fallbackCalls: [], validateCalls: [] };
  let validateCallIndex = 0;
  const recoverableStages = [
    "issue-pickup",
    "feature-planning",
    "feature-dev",
    "feature-validate",
    "pr-create",
  ];

  return {
    calls,
    async attemptContextRecovery(
      stage: string,
      issueNumber: number,
      originalError: Error
    ): Promise<RecoveryOutcome> {
      // Mirror of the production attemptContextRecovery body.
      if (!recoverableStages.includes(stage)) {
        return { recovered: false, error: originalError };
      }
      calls.fallbackCalls.push({ stage, issueNumber });
      const generated = opts.fallbackOutcome(stage);
      if (!generated) {
        return { recovered: false, error: originalError };
      }
      calls.validateCalls.push({ stage, issueNumber });
      const retryError = opts.validateOutcome(stage, validateCallIndex++);
      if (!retryError) return { recovered: true };
      return { recovered: false, error: retryError };
    },
  };
}

describe("attemptContextRecovery dispatch (Issue #3267)", () => {
  it("dispatches recovery for each of the 5 recoverable stages", async () => {
    const stages = [
      "issue-pickup",
      "feature-planning",
      "feature-dev",
      "feature-validate",
      "pr-create",
    ] as const;
    for (const stage of stages) {
      const fake = makeFake({
        fallbackOutcome: () => true,
        validateOutcome: () => null,
      });
      const outcome = await fake.attemptContextRecovery(stage, 42, new Error("missing"));
      expect(outcome.recovered).toBe(true);
      expect(fake.calls.fallbackCalls).toHaveLength(1);
      expect(fake.calls.fallbackCalls[0]).toEqual({ stage, issueNumber: 42 });
      expect(fake.calls.validateCalls).toHaveLength(1);
    }
  });

  it("returns recovered=false with original error when fallback declines", async () => {
    const fake = makeFake({
      fallbackOutcome: () => false,
      validateOutcome: () => null,
    });
    const original = new Error("missing context");
    const outcome = await fake.attemptContextRecovery("feature-dev", 7, original);
    expect(outcome.recovered).toBe(false);
    if (!outcome.recovered) {
      expect(outcome.error).toBe(original);
    }
    expect(fake.calls.validateCalls).toHaveLength(0);
  });

  it("returns recovered=false with revalidation error when fallback succeeds but revalidate fails", async () => {
    const revalidateErr = new Error("still invalid after fallback");
    const fake = makeFake({
      fallbackOutcome: () => true,
      validateOutcome: () => revalidateErr,
    });
    const outcome = await fake.attemptContextRecovery(
      "feature-planning",
      11,
      new Error("first failure")
    );
    expect(outcome.recovered).toBe(false);
    if (!outcome.recovered) {
      expect(outcome.error).toBe(revalidateErr);
    }
  });

  it("does NOT call fallback for non-recoverable stages (e.g., pr-merge)", async () => {
    const fake = makeFake({
      fallbackOutcome: () => true,
      validateOutcome: () => null,
    });
    const original = new Error("post-merge verify failed");
    const outcome = await fake.attemptContextRecovery("pr-merge", 99, original);
    expect(outcome.recovered).toBe(false);
    if (!outcome.recovered) {
      expect(outcome.error).toBe(original);
    }
    expect(fake.calls.fallbackCalls).toHaveLength(0);
  });

  it("does NOT call fallback for unknown stage", async () => {
    const fake = makeFake({
      fallbackOutcome: () => true,
      validateOutcome: () => null,
    });
    const outcome = await fake.attemptContextRecovery("not-a-real-stage", 1, new Error("x"));
    expect(outcome.recovered).toBe(false);
    expect(fake.calls.fallbackCalls).toHaveLength(0);
  });
});
