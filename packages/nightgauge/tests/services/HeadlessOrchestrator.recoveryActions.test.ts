import { beforeEach, describe, expect, it, vi } from "vitest";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";

const mocks = vi.hoisted(() => ({
  executeCommand: vi.fn(),
  uriFile: vi.fn((fsPath: string) => ({ fsPath })),
  showWarningMessage: vi.fn(),
}));

vi.mock("vscode", () => ({
  commands: { executeCommand: mocks.executeCommand },
  Uri: { file: mocks.uriFile },
  window: { showErrorMessage: vi.fn(), showWarningMessage: mocks.showWarningMessage },
  workspace: {
    workspaceFolders: [{ uri: { fsPath: "/mock/repo" } }],
    getConfiguration: vi.fn(() => ({ get: vi.fn() })),
  },
}));

import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import { IpcClient } from "../../src/services/IpcClient";
import type { RecoveryRequiredPayload } from "@nightgauge/sdk";
import type { Logger } from "../../src/utils/logger";
import { fakeCloneLayout, mkFakeCloneLayout } from "../helpers/cloneLayout";

function makeOrchestrator(): HeadlessOrchestrator {
  const logger = {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  } as unknown as Logger;
  return new HeadlessOrchestrator(null, logger);
}

describe("HeadlessOrchestrator recovery actions", () => {
  // run-state.json is the checkout's own singleton, in CHECKOUT (ADR-024 § 7).
  let mockRepoCheckout: string;

  beforeEach(() => {
    vi.clearAllMocks();
    mockRepoCheckout = fakeCloneLayout("/mock/repo").checkout;
  });

  it("opens the run-state directory through VSCode", async () => {
    const orchestrator = makeOrchestrator();

    const result = await orchestrator.runRecoveryAction("open-run-state-directory");

    expect(result).toEqual({ success: true });
    expect(mocks.uriFile).toHaveBeenCalledWith(mockRepoCheckout);
    expect(mocks.executeCommand).toHaveBeenCalledWith(
      "revealFileInOS",
      expect.objectContaining({ fsPath: mockRepoCheckout })
    );
  });

  it("contains failures from the observational action", async () => {
    const orchestrator = makeOrchestrator();
    mocks.executeCommand.mockRejectedValueOnce(new Error("reveal failed"));

    const result = await orchestrator.runRecoveryAction("open-run-state-directory");

    expect(result.success).toBe(false);
    expect(result.error?.message).toBe("reveal failed");
  });

  it.each([
    "discard-run",
    "resume-from-paused-stage",
    "restart-from-beginning",
    "run-producing-stage",
  ] as const)("refuses retired state-changing action %s", async (action) => {
    const orchestrator = makeOrchestrator();

    const result = await orchestrator.runRecoveryAction(action as never);

    expect(result.success).toBe(false);
    expect(result.error?.message).toBe(`Unknown recovery action: ${action}`);
    expect(mocks.executeCommand).not.toHaveBeenCalled();
  });

  it("treats malformed state as no current lifecycle", () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), "nightgauge-recovery-state-"));
    const checkoutDir = mkFakeCloneLayout(root).checkout;
    const statePath = path.join(checkoutDir, "run-state.json");
    const orchestrator = makeOrchestrator();
    orchestrator.setWorktreeOverride(root);
    orchestrator.setMainRepoRoot(root);

    try {
      fs.writeFileSync(statePath, "{malformed state");
      expect((orchestrator as any).readRecoveryRunStateView(793).lifecycle).toBe("none");
    } finally {
      fs.rmSync(root, { recursive: true, force: true });
    }
  });

  it("does not attribute a foreign run-state lifecycle to the requested issue", () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), "nightgauge-foreign-recovery-state-"));
    const checkoutDir = mkFakeCloneLayout(root).checkout;
    fs.writeFileSync(
      path.join(checkoutDir, "run-state.json"),
      JSON.stringify({ issue_number: 794, state: "running" })
    );
    const orchestrator = makeOrchestrator();
    orchestrator.setWorktreeOverride(root);
    orchestrator.setMainRepoRoot(root);

    try {
      expect((orchestrator as any).readRecoveryRunStateView(793).lifecycle).toBe("none");
    } finally {
      fs.rmSync(root, { recursive: true, force: true });
    }
  });
  it("reads a linked worktree's lifecycle from its own CHECKOUT", () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), "nightgauge-recovery-wt-"));
    const main = path.join(root, "main");
    const wt = path.join(root, "wt");
    const common = path.join(main, ".git");
    mkFakeCloneLayout(main);
    const linked = mkFakeCloneLayout(wt, common, path.join(common, "worktrees", "wt"));
    fs.writeFileSync(
      path.join(common, "nightgauge-worktree", "run-state.json"),
      JSON.stringify({ issue_number: 795, state: "running" })
    );
    const orchestrator = makeOrchestrator();
    orchestrator.setWorktreeOverride(wt);
    orchestrator.setMainRepoRoot(main);

    try {
      // The main checkout's run-state is not the worktree's.
      expect((orchestrator as any).readRecoveryRunStateView(795).lifecycle).toBe("none");
      fs.writeFileSync(
        path.join(linked.checkout, "run-state.json"),
        JSON.stringify({ issue_number: 795, state: "paused" })
      );
      expect((orchestrator as any).readRecoveryRunStateView(795).lifecycle).toBe("paused");
    } finally {
      fs.rmSync(root, { recursive: true, force: true });
    }
  });
});

describe("HeadlessOrchestrator state-changing recovery (#794)", () => {
  const identity = {
    repo: "o/r",
    issue: 42,
    runId: "0192f8a0-0000-7000-8000-000000000001",
    branch: "feat/42-x",
    stage: "feature-dev",
    snapshotSha256: "a".repeat(64),
  };
  const payload: RecoveryRequiredPayload = {
    issueNumber: 42,
    triggeringStage: "feature-dev",
    errorKind: "RUN_STATE_MISSING",
    errorDetail: "x",
    runState: "paused",
    availableActions: ["restart", "cancel"],
    recoveryIdentity: identity,
  };

  function fakeIpc(supported: boolean, calls: string[]) {
    const ipc = {
      supportsFeature: vi.fn(() => supported),
      pipelineRecoveryClaim: vi.fn(async () => {
        calls.push("claim");
        return { token: "tok", expiresAt: "" };
      }),
      pipelineRecoveryExecute: vi.fn(async () => {
        calls.push("execute");
        return { action: "restart", completedSteps: [], skippedSteps: [] };
      }),
      pipelineRecoveryRelease: vi.fn(async () => {
        calls.push("release");
      }),
    };
    vi.spyOn(IpcClient, "getInstance").mockReturnValue(ipc as never);
    return ipc;
  }

  beforeEach(() => {
    vi.restoreAllMocks();
    mocks.showWarningMessage.mockReset();
  });

  it("confirms in a host modal, then claims, then executes", async () => {
    const calls: string[] = [];
    fakeIpc(true, calls);
    mocks.showWarningMessage.mockImplementation(async () => {
      calls.push("confirm");
      return "Restart";
    });

    const result = await makeOrchestrator().runRecoveryAction("restart", payload);

    expect(result).toEqual({ success: true });
    expect(calls).toEqual(["confirm", "claim", "execute"]);
    expect(mocks.showWarningMessage.mock.calls[0][1]).toMatchObject({ modal: true });
  });

  it("does not claim or execute when the operator declines", async () => {
    const calls: string[] = [];
    fakeIpc(true, calls);
    mocks.showWarningMessage.mockResolvedValue(undefined);

    await makeOrchestrator().runRecoveryAction("restart", payload);

    expect(calls).toEqual([]);
  });

  it("refuses when the binary does not advertise recovery_transactions", async () => {
    const calls: string[] = [];
    fakeIpc(false, calls);

    const result = await makeOrchestrator().runRecoveryAction("restart", payload);

    expect(result.success).toBe(false);
    expect(mocks.showWarningMessage).not.toHaveBeenCalled();
    expect(calls).toEqual([]);
  });

  it("refuses without a run identity", async () => {
    const calls: string[] = [];
    fakeIpc(true, calls);

    const result = await makeOrchestrator().runRecoveryAction("restart", {
      ...payload,
      recoveryIdentity: undefined,
    });

    expect(result.success).toBe(false);
    expect(calls).toEqual([]);
  });

  it("releases the claim when execution fails", async () => {
    const calls: string[] = [];
    const ipc = fakeIpc(true, calls);
    ipc.pipelineRecoveryExecute.mockRejectedValueOnce(new Error("step failed"));
    mocks.showWarningMessage.mockResolvedValue("Restart");

    const result = await makeOrchestrator().runRecoveryAction("restart", payload);

    expect(result.success).toBe(false);
    expect(result.error?.message).toBe("step failed");
    expect(calls).toEqual(["claim", "release"]);
  });
});
