/**
 * HeadlessOrchestrator.epicBaseBranch.test.ts
 *
 * Pins the fail-closed epic base_branch contract. A sub-issue of an epic must
 * target the epic integration branch, never main:
 *   - if the epic branch exists → retarget base_branch to it
 *   - if it doesn't → CREATE it (deterministic `epic create-branch`) and retarget
 *   - if creation fails → return { ok: false } so the caller fails the stage
 *     instead of silently merging the sub-issue to main
 *   - non-epic issues, or unconfirmable parents, return { ok: true } (no block)
 *
 * Root cause this guards: the Go scheduler created the epic branch and this TS
 * method only retargeted to it, but the extension's autonomous slots never run
 * the Go scheduler — so the branch was never created, this fell open to main,
 * and acmeapp-platform#6/#7 landed on main individually.
 */

import { describe, it, expect, beforeEach, vi } from "vitest";
import * as fs from "fs";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { Logger } from "../../src/utils/logger";

vi.mock("../../src/utils/skillRunner", () => ({
  hasActiveProcess: vi.fn().mockReturnValue(false),
  killAllActiveProcesses: vi.fn(),
  getActiveInteractiveProcess: vi.fn().mockReturnValue(null),
  runStageSkillHeadless: vi.fn(),
  getNextStage: vi.fn(),
  getStageLabel: vi.fn((s: string) => s),
  resolveModel: vi.fn().mockReturnValue({ model: "claude-sonnet-4-6", source: "default" }),
}));

vi.mock("../../src/services/BinaryResolver", () => ({
  BinaryResolver: { fromVSCode: () => ({ resolve: async () => "/fake/nightgauge" }) },
}));

const { parentNumber, existingEpicBranch, createReturnsBranch, writtenBase, createBranchCalls } =
  vi.hoisted(() => ({
    parentNumber: { value: "null" as string },
    existingEpicBranch: { value: "" as string },
    createReturnsBranch: { value: "epic/3-backend-contract" as string },
    writtenBase: { value: "" as string },
    createBranchCalls: { value: 0 },
  }));

vi.mock("child_process", async () => {
  const actual = await vi.importActual<typeof import("child_process")>("child_process");
  const kCustom = Symbol.for("nodejs.util.promisify.custom");
  const authStatus = "Logged in to github.com account testuser\n  Token scopes: 'repo'";

  const execMock: any = vi.fn();
  execMock[kCustom] = () => Promise.resolve({ stdout: authStatus, stderr: "" });

  const execFileMock: any = vi.fn();
  execFileMock[kCustom] = (cmd: string, args: string[]) => {
    // nightgauge epic create-branch <n> --json
    if (typeof cmd === "string" && cmd.includes("nightgauge") && args?.[0] === "epic") {
      createBranchCalls.value++;
      if (!createReturnsBranch.value) {
        return Promise.resolve({ stdout: JSON.stringify({ created: false }), stderr: "" });
      }
      return Promise.resolve({
        stdout: JSON.stringify({ branch: createReturnsBranch.value, created: true }),
        stderr: "",
      });
    }
    // gh api graphql → parent number
    if (args?.[0] === "api" && args?.includes("graphql")) {
      return Promise.resolve({ stdout: parentNumber.value, stderr: "" });
    }
    // git ls-remote --heads origin epic/<n>-*
    if (args?.[0] === "ls-remote") {
      const out = existingEpicBranch.value ? `abc123\trefs/heads/${existingEpicBranch.value}` : "";
      return Promise.resolve({ stdout: out, stderr: "" });
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

vi.mock("fs", async () => {
  const actual = await vi.importActual<typeof import("fs")>("fs");
  return {
    ...actual,
    existsSync: vi.fn().mockReturnValue(true),
    readFileSync: vi.fn().mockImplementation((p: string) => {
      if (typeof p === "string" && p.includes("issue-")) {
        return JSON.stringify({ base_branch: "main" });
      }
      return "{}";
    }),
    writeFileSync: vi.fn().mockImplementation((_p: string, content: string) => {
      try {
        writtenBase.value = (JSON.parse(content) as { base_branch?: string }).base_branch ?? "";
      } catch {
        /* ignore */
      }
    }),
  };
});

function createMockStateService(): PipelineStateService {
  return { getState: vi.fn().mockResolvedValue(null) } as unknown as PipelineStateService;
}

describe("HeadlessOrchestrator.enforceEpicBaseBranch (fail-closed)", () => {
  let logger: Logger;

  beforeEach(() => {
    vi.clearAllMocks();
    parentNumber.value = "null";
    existingEpicBranch.value = "";
    createReturnsBranch.value = "epic/3-backend-contract";
    writtenBase.value = "";
    createBranchCalls.value = 0;
    delete process.env.NIGHTGAUGE_PIPELINE_AUTO_CREATE_EPIC_BRANCH;
    logger = { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() } as unknown as Logger;
  });

  function makeOrchestrator(): any {
    const o = new HeadlessOrchestrator(createMockStateService(), logger, { contextFileWaitMs: 0 });
    o.setRepoOverride("nightgauge/acmeapp-platform");
    return o;
  }

  it("returns ok and does nothing for a non-epic issue", async () => {
    parentNumber.value = "null";
    const o = makeOrchestrator();
    const res = await o.enforceEpicBaseBranch(7);
    expect(res.ok).toBe(true);
    expect(createBranchCalls.value).toBe(0);
    expect(writtenBase.value).toBe(""); // no rewrite
  });

  it("retargets base_branch to an existing epic branch without creating one", async () => {
    parentNumber.value = "3";
    existingEpicBranch.value = "epic/3-backend-contract";
    const o = makeOrchestrator();
    const res = await o.enforceEpicBaseBranch(7);
    expect(res.ok).toBe(true);
    expect(createBranchCalls.value).toBe(0);
    expect(writtenBase.value).toBe("epic/3-backend-contract");
  });

  it("creates the epic branch when missing and retargets to it (the acmeapp case)", async () => {
    parentNumber.value = "3";
    existingEpicBranch.value = ""; // not on remote yet
    createReturnsBranch.value = "epic/3-backend-contract";
    const o = makeOrchestrator();
    const res = await o.enforceEpicBaseBranch(7);
    expect(res.ok).toBe(true);
    expect(createBranchCalls.value).toBe(1);
    expect(writtenBase.value).toBe("epic/3-backend-contract");
  });

  it("fails closed when the epic branch cannot be created", async () => {
    parentNumber.value = "3";
    existingEpicBranch.value = "";
    createReturnsBranch.value = ""; // create returns no branch → failure
    const o = makeOrchestrator();
    const res = await o.enforceEpicBaseBranch(7);
    expect(res.ok).toBe(false);
    expect(res.error).toMatch(/epic #3/);
    expect(writtenBase.value).toBe(""); // never retargeted to main
  });

  it("preserves the historical fall-through when auto_create_epic_branch is disabled", async () => {
    parentNumber.value = "3";
    existingEpicBranch.value = "";
    process.env.NIGHTGAUGE_PIPELINE_AUTO_CREATE_EPIC_BRANCH = "false";
    const o = makeOrchestrator();
    const res = await o.enforceEpicBaseBranch(7);
    expect(res.ok).toBe(true); // does not block when explicitly disabled
    expect(createBranchCalls.value).toBe(0);
  });
});
