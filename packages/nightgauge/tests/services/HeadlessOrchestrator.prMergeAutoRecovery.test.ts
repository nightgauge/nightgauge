/**
 * HeadlessOrchestrator.prMergeAutoRecovery.test.ts
 *
 * Pins the agent_gave_up auto-recovery contract: when diagnosePrMergeBlocker
 * classifies a PR as `agent_gave_up` (PR verifiably MERGEABLE + CLEAN + green
 * + no review block, but the pr-merge agent ended its session without
 * merging), the reconciliation path must finish the job deterministically via
 * attemptDeterministicPrMerge — the binary-independent backstop to the in-stage
 * #3259 fallback that skips when the Go binary gate can't resolve.
 *
 * This is the acmeapp #8 / PR #18 case: $2.88 spent, paused, then merged by
 * hand because the in-stage fallback never ran.
 */

import { describe, it, expect, beforeEach, vi } from "vitest";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { Logger } from "../../src/utils/logger";

vi.mock("../../src/utils/skillRunner", () => ({
  hasActiveProcess: vi.fn().mockReturnValue(false),
  killAllActiveProcesses: vi.fn(),
  getActiveInteractiveProcess: vi.fn().mockReturnValue(null),
  runStageSkillHeadless: vi.fn(),
  getNextStage: vi.fn(),
  getStageLabel: vi.fn((stage: string) => stage),
  resolveModel: vi.fn().mockReturnValue({ model: "claude-sonnet-4-6", source: "default" }),
}));

// Mutable knobs shared between the hoisted child_process mock and tests.
const { mergeShouldFail, prStateAfterMerge, issueState, prMergeCalls, issueCloseCalls } =
  vi.hoisted(() => ({
    mergeShouldFail: { value: false },
    prStateAfterMerge: { value: "MERGED" },
    issueState: { value: "CLOSED" },
    prMergeCalls: { value: 0 },
    issueCloseCalls: { value: 0 },
  }));

vi.mock("child_process", async () => {
  const actual = await vi.importActual<typeof import("child_process")>("child_process");
  const kCustom = Symbol.for("nodejs.util.promisify.custom");

  const authStatus =
    "Logged in to github.com account testuser (keyring)\n" +
    "  Token: gho_fake\n  Token scopes: 'gist', 'read:org', 'repo', 'workflow'";

  const execMock: any = vi.fn();
  execMock[kCustom] = () => Promise.resolve({ stdout: authStatus, stderr: "" });

  const execFileMock: any = vi.fn();
  execFileMock[kCustom] = (_cmd: string, args: string[]) => {
    // gh pr merge <n> --squash --delete-branch
    if (args && args[0] === "pr" && args[1] === "merge") {
      prMergeCalls.value++;
      if (mergeShouldFail.value) {
        const err: any = new Error("merge failed: branch protection");
        err.code = 1;
        return Promise.reject(err);
      }
      return Promise.resolve({ stdout: "", stderr: "" });
    }
    // gh pr view <n> --json state -q .state
    if (args && args[0] === "pr" && args[1] === "view") {
      return Promise.resolve({ stdout: prStateAfterMerge.value, stderr: "" });
    }
    // gh issue close <n> ...
    if (args && args[0] === "issue" && args[1] === "close") {
      issueCloseCalls.value++;
      return Promise.resolve({ stdout: "", stderr: "" });
    }
    // gh issue view <n> --json state -q .state
    if (args && args[0] === "issue" && args[1] === "view") {
      return Promise.resolve({ stdout: issueState.value, stderr: "" });
    }
    return Promise.resolve({ stdout: "", stderr: "" });
  };

  return {
    ...actual,
    exec: execMock,
    execFile: execFileMock,
    execSync: vi.fn().mockReturnValue(authStatus),
    execFileSync: vi.fn().mockReturnValue(authStatus),
  };
});

function createMockStateService(): PipelineStateService {
  return {
    getState: vi.fn().mockResolvedValue(null),
    batchUpdate: vi.fn().mockResolvedValue(undefined),
  } as unknown as PipelineStateService;
}

describe("HeadlessOrchestrator agent_gave_up auto-recovery (attemptDeterministicPrMerge)", () => {
  let mockLogger: Logger;

  beforeEach(() => {
    vi.clearAllMocks();
    mergeShouldFail.value = false;
    prStateAfterMerge.value = "MERGED";
    issueState.value = "CLOSED";
    prMergeCalls.value = 0;
    issueCloseCalls.value = 0;
    mockLogger = {
      info: vi.fn(),
      warn: vi.fn(),
      error: vi.fn(),
      debug: vi.fn(),
    } as unknown as Logger;
  });

  function makeOrchestrator(): HeadlessOrchestrator {
    return new HeadlessOrchestrator(createMockStateService(), mockLogger, {
      contextFileWaitMs: 0,
    });
  }

  it("merges the mergeable PR and reports recovery when post-merge state is MERGED", async () => {
    const orchestrator = makeOrchestrator() as any;
    const recovered = await orchestrator.attemptDeterministicPrMerge(18, 8);

    expect(recovered).toBe(true);
    expect(prMergeCalls.value).toBe(1);
    // Issue already CLOSED by the merge's closing keyword — no explicit close.
    expect(issueCloseCalls.value).toBe(0);
    const infoCalls = vi.mocked(mockLogger.info).mock.calls;
    expect(
      infoCalls.some(
        (a) => typeof a[0] === "string" && a[0].includes("PR merged the agent left open")
      )
    ).toBe(true);
  });

  it("closes the linked issue when the PR body lacked a closing keyword", async () => {
    issueState.value = "OPEN"; // PR merged but issue still open
    const orchestrator = makeOrchestrator() as any;
    const recovered = await orchestrator.attemptDeterministicPrMerge(18, 8);

    expect(recovered).toBe(true);
    expect(issueCloseCalls.value).toBe(1);
  });

  it("returns false (falls through to pause) when the merge command errors", async () => {
    mergeShouldFail.value = true;
    const orchestrator = makeOrchestrator() as any;
    const recovered = await orchestrator.attemptDeterministicPrMerge(18, 8);

    expect(recovered).toBe(false);
    expect(prMergeCalls.value).toBe(1);
    // Never trusts the exit code — no post-merge confirmation past a failure.
    const warnCalls = vi.mocked(mockLogger.warn).mock.calls;
    expect(
      warnCalls.some((a) => typeof a[0] === "string" && a[0].includes("deterministic merge failed"))
    ).toBe(true);
  });

  it("returns false when the merge command 'succeeds' but PR is not MERGED afterward", async () => {
    // The #3691 lesson: never trust the merge command's exit code alone.
    prStateAfterMerge.value = "OPEN";
    const orchestrator = makeOrchestrator() as any;
    const recovered = await orchestrator.attemptDeterministicPrMerge(18, 8);

    expect(recovered).toBe(false);
    expect(issueCloseCalls.value).toBe(0);
  });
});
