/**
 * Issue #3927: Deterministic pr-create fallback in HeadlessOrchestrator.
 *
 * tryDeterministicCreateFallback() mirrors the pr-merge fallback (#3259): when
 * the pr-create skill exits 0 but no open PR exists, it pushes the feature
 * branch and opens the PR itself rather than depending on an LLM retry that may
 * no-op again. These tests drive the private method directly with a mocked
 * git/gh layer.
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
  resolveModel: vi.fn().mockReturnValue({ model: "claude-sonnet-4-6", source: "default" }),
}));

vi.mock("../../src/services/BinaryResolver", () => ({
  BinaryResolver: { fromVSCode: () => ({ resolve: async () => "/fake/nightgauge" }) },
}));

const { branchName, preExistingPrs, commitsAhead, pushFails, createFails, prCreated, calls } =
  vi.hoisted(() => ({
    branchName: { value: "feat/42-add-thing" },
    preExistingPrs: { value: [] as Array<{ number: number }> },
    commitsAhead: { value: "2" },
    pushFails: { value: false },
    createFails: { value: false },
    prCreated: { value: false },
    calls: { value: [] as string[][] },
  }));

vi.mock("child_process", async () => {
  const actual = await vi.importActual<typeof import("child_process")>("child_process");
  const kCustom = Symbol.for("nodejs.util.promisify.custom");

  const execMock: any = vi.fn();
  execMock[kCustom] = () => Promise.resolve({ stdout: "", stderr: "" });

  const execFileMock: any = vi.fn();
  execFileMock[kCustom] = (cmd: string, args: string[]) => {
    const a = args || [];
    calls.value.push([cmd, ...a]);

    // git rev-parse --abbrev-ref HEAD → feature branch
    if (cmd === "git" && a[0] === "rev-parse") {
      return Promise.resolve({ stdout: `${branchName.value}\n`, stderr: "" });
    }
    // gh pr list --head <branch> --state open --json number
    if (cmd === "gh" && a[0] === "pr" && a[1] === "list") {
      const prs = prCreated.value ? [{ number: 99 }] : preExistingPrs.value;
      return Promise.resolve({ stdout: JSON.stringify(prs), stderr: "" });
    }
    // gh repo view --json defaultBranchRef -q .defaultBranchRef.name
    if (cmd === "gh" && a[0] === "repo" && a[1] === "view") {
      return Promise.resolve({ stdout: "main\n", stderr: "" });
    }
    // git rev-list --count origin/main..HEAD
    if (cmd === "git" && a[0] === "rev-list") {
      return Promise.resolve({ stdout: `${commitsAhead.value}\n`, stderr: "" });
    }
    // git push -u origin <branch>
    if (cmd === "git" && a[0] === "push") {
      if (pushFails.value) {
        return Promise.reject(
          Object.assign(new Error("push failed"), { stderr: "remote rejected" })
        );
      }
      return Promise.resolve({ stdout: "", stderr: "" });
    }
    // gh issue view <n> --json title -q .title
    if (cmd === "gh" && a[0] === "issue" && a[1] === "view") {
      return Promise.resolve({ stdout: "Add thing\n", stderr: "" });
    }
    // gh pr create ...
    if (cmd === "gh" && a[0] === "pr" && a[1] === "create") {
      if (createFails.value) {
        return Promise.reject(
          Object.assign(new Error("create failed"), {
            stderr: "no commits between main and branch",
          })
        );
      }
      prCreated.value = true;
      return Promise.resolve({ stdout: "https://github.com/o/r/pull/99\n", stderr: "" });
    }
    return Promise.resolve({ stdout: "{}", stderr: "" });
  };

  return { ...actual, exec: execMock, execFile: execFileMock };
});

function makeLogger(): Logger {
  return { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() } as unknown as Logger;
}

// Invoke the private method under test.
function callFallback(issueNumber = 42) {
  const orch = new HeadlessOrchestrator(null, makeLogger(), { contextFileWaitMs: 0 });
  return (
    orch as unknown as {
      tryDeterministicCreateFallback: (
        n: number,
        cwd: string
      ) => Promise<{ created: boolean; reason?: string }>;
    }
  ).tryDeterministicCreateFallback(issueNumber, "/tmp/worktree");
}

function countCalls(predicate: (c: string[]) => boolean): number {
  return calls.value.filter(predicate).length;
}

describe("tryDeterministicCreateFallback (#3927)", () => {
  beforeEach(() => {
    branchName.value = "feat/42-add-thing";
    preExistingPrs.value = [];
    commitsAhead.value = "2";
    pushFails.value = false;
    createFails.value = false;
    prCreated.value = false;
    calls.value = [];
  });

  it("pushes the branch and opens a PR when commits exist and no PR is present", async () => {
    const result = await callFallback();
    expect(result.created).toBe(true);
    expect(countCalls((c) => c[0] === "git" && c[1] === "push")).toBe(1);
    expect(countCalls((c) => c[0] === "gh" && c[1] === "pr" && c[2] === "create")).toBe(1);
  });

  it("uses --body (not a heredoc) and includes the closing keyword + issue title", async () => {
    await callFallback();
    const create = calls.value.find((c) => c[0] === "gh" && c[1] === "pr" && c[2] === "create");
    expect(create).toBeDefined();
    expect(create).toContain("--body");
    expect(create).toContain("--title");
    const body = create![create!.indexOf("--body") + 1];
    expect(body).toContain("Closes #42");
    const title = create![create!.indexOf("--title") + 1];
    expect(title).toBe("Add thing");
  });

  it("short-circuits to created:true without pushing when an open PR already exists", async () => {
    preExistingPrs.value = [{ number: 50 }];
    const result = await callFallback();
    expect(result.created).toBe(true);
    expect(countCalls((c) => c[0] === "git" && c[1] === "push")).toBe(0);
    expect(countCalls((c) => c[0] === "gh" && c[1] === "pr" && c[2] === "create")).toBe(0);
  });

  it("refuses to open a PR from a base branch", async () => {
    branchName.value = "main";
    const result = await callFallback();
    expect(result.created).toBe(false);
    expect(result.reason).toMatch(/base\/detached branch/i);
    expect(countCalls((c) => c[0] === "git" && c[1] === "push")).toBe(0);
  });

  it("does not create a PR when the branch has no commits ahead of base", async () => {
    commitsAhead.value = "0";
    const result = await callFallback();
    expect(result.created).toBe(false);
    expect(result.reason).toMatch(/no commits ahead/i);
    expect(countCalls((c) => c[0] === "gh" && c[1] === "pr" && c[2] === "create")).toBe(0);
  });

  // Issue #317: the confirmed-zero-commits case must carry the stable
  // `[no-changes-produced]` marker so ClassifyTerminalKind/classifyTerminalKind
  // record the honest "nothing to commit" kind instead of the generic
  // subagent_crash fallback.
  it("stamps the [no-changes-produced] marker when commits-ahead is confirmed zero (#317)", async () => {
    commitsAhead.value = "0";
    const result = await callFallback();
    expect(result.created).toBe(false);
    expect(result.reason).toContain("[no-changes-produced]");
  });

  it("does NOT stamp the marker when the commit count is unparseable (inconclusive, not confirmed-zero)", async () => {
    commitsAhead.value = "not-a-number";
    const result = await callFallback();
    expect(result.created).toBe(false);
    expect(result.reason).not.toContain("[no-changes-produced]");
    expect(result.reason).toMatch(/could not parse commit count/i);
  });

  it("returns created:false with a reason when the push fails", async () => {
    pushFails.value = true;
    const result = await callFallback();
    expect(result.created).toBe(false);
    expect(result.reason).toMatch(/push failed/i);
    expect(countCalls((c) => c[0] === "gh" && c[1] === "pr" && c[2] === "create")).toBe(0);
  });

  it("returns created:false with a reason when gh pr create fails", async () => {
    createFails.value = true;
    const result = await callFallback();
    expect(result.created).toBe(false);
    expect(result.reason).toMatch(/gh pr create failed/i);
  });
});
