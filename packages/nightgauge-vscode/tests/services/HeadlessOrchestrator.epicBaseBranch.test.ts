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
 *   - a parent in ANOTHER repository has no epic branch here: the base branch
 *     is kept and nothing is looked up or created (#2377)
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
import { isIssueJsonPath } from "../helpers/issueFilePredicates";

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

const {
  parentJson,
  graphqlQueries,
  existingEpicBranch,
  lsRemoteCalls,
  createReturnsBranch,
  writtenBase,
  createBranchCalls,
  contextBase,
} = vi.hoisted(() => ({
  // What `gh api graphql --jq .data.repository.issue.parent` prints: the
  // parent as compact JSON, or an empty line when there is none.
  parentJson: { value: "" as string },
  graphqlQueries: { value: [] as string[] },
  existingEpicBranch: { value: "" as string },
  lsRemoteCalls: { value: 0 },
  createReturnsBranch: { value: "epic/3-backend-contract" as string },
  writtenBase: { value: "" as string },
  createBranchCalls: { value: 0 },
  // `base_branch` the intercepted issue context file hands back. Per-test so
  // the mock's return value is observable, not a constant nobody can see.
  contextBase: { value: "main" as string },
}));

/** The orchestrator below runs for nightgauge/acmeapp-platform. */
const THIS_REPO = "nightgauge/acmeapp-platform";

function setParent(number: number, repo: string = THIS_REPO): void {
  parentJson.value = JSON.stringify({ number, repository: { nameWithOwner: repo } });
}

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
    // gh api graphql → the parent, with its repository
    if (args?.[0] === "api" && args?.includes("graphql")) {
      graphqlQueries.value.push(args.join(" "));
      return Promise.resolve({ stdout: parentJson.value + "\n", stderr: "" });
    }
    // git ls-remote --heads origin epic/<n>-*
    if (args?.[0] === "ls-remote") {
      lsRemoteCalls.value++;
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
      // Basename-anchored (#426): the old substring form also matched the
      // ambient checkout path (e.g. .nightgauge/worktrees/issue-422/...), so
      // every unrelated read returned an issue context document.
      if (isIssueJsonPath(p)) {
        return JSON.stringify({ base_branch: contextBase.value });
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
    parentJson.value = "";
    graphqlQueries.value = [];
    existingEpicBranch.value = "";
    lsRemoteCalls.value = 0;
    createReturnsBranch.value = "epic/3-backend-contract";
    writtenBase.value = "";
    createBranchCalls.value = 0;
    contextBase.value = "main";
    delete process.env.NIGHTGAUGE_PIPELINE_AUTO_CREATE_EPIC_BRANCH;
    logger = { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() } as unknown as Logger;
  });

  function makeOrchestrator(): any {
    const o = new HeadlessOrchestrator(createMockStateService(), logger, { contextFileWaitMs: 0 });
    o.setRepoOverride(THIS_REPO);
    return o;
  }

  it("returns ok and does nothing for a non-epic issue", async () => {
    parentJson.value = "";
    const o = makeOrchestrator();
    const res = await o.enforceEpicBaseBranch(7);
    expect(res.ok).toBe(true);
    expect(createBranchCalls.value).toBe(0);
    expect(writtenBase.value).toBe(""); // no rewrite
  });

  it("retargets base_branch to an existing epic branch without creating one", async () => {
    setParent(3);
    existingEpicBranch.value = "epic/3-backend-contract";
    const o = makeOrchestrator();
    const res = await o.enforceEpicBaseBranch(7);
    expect(res.ok).toBe(true);
    expect(createBranchCalls.value).toBe(0);
    expect(writtenBase.value).toBe("epic/3-backend-contract");
  });

  it("creates the epic branch when missing and retargets to it (the acmeapp case)", async () => {
    setParent(3);
    existingEpicBranch.value = ""; // not on remote yet
    createReturnsBranch.value = "epic/3-backend-contract";
    const o = makeOrchestrator();
    const res = await o.enforceEpicBaseBranch(7);
    expect(res.ok).toBe(true);
    expect(createBranchCalls.value).toBe(1);
    expect(writtenBase.value).toBe("epic/3-backend-contract");
  });

  it("fails closed when the epic branch cannot be created", async () => {
    setParent(3);
    existingEpicBranch.value = "";
    createReturnsBranch.value = ""; // create returns no branch → failure
    const o = makeOrchestrator();
    const res = await o.enforceEpicBaseBranch(7);
    expect(res.ok).toBe(false);
    expect(res.error).toMatch(/epic #3/);
    expect(writtenBase.value).toBe(""); // never retargeted to main
  });

  it("leaves a context file that already targets an epic branch untouched", async () => {
    // Makes the intercepted read load-bearing (#426). The mock hands back a
    // base_branch distinguishable from both "main" and the epic branch this
    // parent would resolve to, and the assertions below only hold if that
    // value was actually consumed: with the fs predicate matching nothing,
    // readFileSync falls through to "{}", currentBase defaults to "main",
    // parent #3 resolves to epic/3-backend-contract, and writtenBase is
    // rewritten — so this test fails rather than silently passing.
    contextBase.value = "epic/99-preexisting-target";
    setParent(3);
    existingEpicBranch.value = "epic/3-backend-contract";

    const o = makeOrchestrator();
    const res = await o.enforceEpicBaseBranch(7);

    expect(res.ok).toBe(true);
    expect(writtenBase.value).toBe(""); // short-circuited before any rewrite
    expect(createBranchCalls.value).toBe(0);
  });

  // #2377: epic/3-* in this repository is the branch of ITS #3. Basing a
  // sub-issue of another repository's #3 on it, or creating one beside it,
  // strands the work on a branch the epic's PR (opened in the epic's own
  // repository) never merges.
  it("keeps the base branch of a sub-issue whose epic lives in another repository", async () => {
    setParent(3, "nightgauge/acmeapp-backend");
    existingEpicBranch.value = "epic/3-this-repos-own-issue";
    const o = makeOrchestrator();
    const res = await o.enforceEpicBaseBranch(7);
    expect(res.ok).toBe(true);
    expect(graphqlQueries.value[0]).toContain("repository { nameWithOwner }");
    expect(lsRemoteCalls.value).toBe(0); // no epic/3-* looked up here
    expect(createBranchCalls.value).toBe(0); // and none created
    expect(writtenBase.value).toBe(""); // base_branch left as written
  });

  it("matches the parent's repository case-insensitively", async () => {
    setParent(3, "NightGauge/AcmeApp-Platform");
    existingEpicBranch.value = "epic/3-backend-contract";
    const o = makeOrchestrator();
    const res = await o.enforceEpicBaseBranch(7);
    expect(res.ok).toBe(true);
    expect(writtenBase.value).toBe("epic/3-backend-contract");
  });

  it("preserves the historical fall-through when auto_create_epic_branch is disabled", async () => {
    setParent(3);
    existingEpicBranch.value = "";
    process.env.NIGHTGAUGE_PIPELINE_AUTO_CREATE_EPIC_BRANCH = "false";
    const o = makeOrchestrator();
    const res = await o.enforceEpicBaseBranch(7);
    expect(res.ok).toBe(true); // does not block when explicitly disabled
    expect(createBranchCalls.value).toBe(0);
  });
});
