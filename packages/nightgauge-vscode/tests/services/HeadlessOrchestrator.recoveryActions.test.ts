import { beforeEach, describe, expect, it, vi } from "vitest";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";

const mocks = vi.hoisted(() => ({
  executeCommand: vi.fn(),
  uriFile: vi.fn((fsPath: string) => ({ fsPath })),
}));

vi.mock("vscode", () => ({
  commands: { executeCommand: mocks.executeCommand },
  Uri: { file: mocks.uriFile },
  window: { showErrorMessage: vi.fn() },
  workspace: {
    workspaceFolders: [{ uri: { fsPath: "/mock/repo" } }],
    getConfiguration: vi.fn(() => ({ get: vi.fn() })),
  },
}));

import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { Logger } from "../../src/utils/logger";

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
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("opens the run-state directory through VSCode", async () => {
    const orchestrator = makeOrchestrator();

    const result = await orchestrator.runRecoveryAction("open-run-state-directory");

    expect(result).toEqual({ success: true });
    expect(mocks.uriFile).toHaveBeenCalledWith("/mock/repo/.nightgauge/pipeline");
    expect(mocks.executeCommand).toHaveBeenCalledWith(
      "revealFileInOS",
      expect.objectContaining({ fsPath: "/mock/repo/.nightgauge/pipeline" })
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
    const pipelineDir = path.join(root, ".nightgauge", "pipeline");
    fs.mkdirSync(pipelineDir, { recursive: true });
    const statePath = path.join(pipelineDir, "run-state.json");
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
    const pipelineDir = path.join(root, ".nightgauge", "pipeline");
    fs.mkdirSync(pipelineDir, { recursive: true });
    fs.writeFileSync(
      path.join(pipelineDir, "run-state.json"),
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
});
