/**
 * Issue #3927: Deterministic pr-create fallback in HeadlessOrchestrator.
 *
 * tryDeterministicCreateFallback() mirrors the pr-merge fallback (#3259): when
 * the pr-create skill exits 0 but no open PR exists, it pushes the feature
 * branch and opens the PR itself rather than depending on an LLM retry that may
 * no-op again. These tests drive the private method directly with a mocked
 * git/gh layer.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";
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

// The pipeline state dir resolves through the binary; point it at a temp dir
// so the gate-stop record and validate verdicts are real files (#2677).
vi.mock("../../src/utils/cloneLayout", async () => {
  const actual = await vi.importActual<typeof import("../../src/utils/cloneLayout")>(
    "../../src/utils/cloneLayout"
  );
  return { ...actual, pipelineStateDir: () => stateDir.value };
});

vi.mock("../../src/services/BinaryResolver", () => ({
  BinaryResolver: { fromVSCode: () => ({ resolve: async () => binaryPath.value }) },
}));

const {
  stateDir,
  headSha,
  binaryPath,
  branchName,
  preExistingPrs,
  commitsAhead,
  pushFails,
  createFails,
  prCreated,
  calls,
} = vi.hoisted(() => ({
  stateDir: { value: "" },
  headSha: { value: "abc123" },
  binaryPath: { value: "/fake/nightgauge" as string | null },
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

    // git rev-parse HEAD → commit sha (gate-stop record comparison, #2677)
    if (cmd === "git" && a[0] === "rev-parse" && a[1] === "HEAD") {
      return Promise.resolve({ stdout: `${headSha.value}\n`, stderr: "" });
    }
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
      if (a.includes("nameWithOwner")) {
        return Promise.resolve({ stdout: "o/r\n", stderr: "" });
      }
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
    // nightgauge pr create ... (#2506: never ambient gh pr create)
    if (cmd === "/fake/nightgauge" && a[0] === "pr" && a[1] === "create") {
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

// Drive the caller so the execution-path recording is covered, not just the
// fallback's return value. The mocked binary answers the gate call with `{}`,
// which reads as passed=false — the recovery branch.
function makeOrchestratorForRecovery() {
  const orch = new HeadlessOrchestrator(null, makeLogger(), { contextFileWaitMs: 0 });
  return {
    orch,
    verify: (n = 42) =>
      (
        orch as unknown as {
          verifyPostCreateState: (n: number) => Promise<Error | null>;
        }
      ).verifyPostCreateState(n),
    paths: () =>
      (
        orch as unknown as {
          stageExecutionPaths: Map<string, { path: string; puntReason?: string }>;
        }
      ).stageExecutionPaths,
  };
}

describe("execution_path attribution when the fallback recovers (#122)", () => {
  beforeEach(() => {
    binaryPath.value = "/fake/nightgauge";
    branchName.value = "feat/42-add-thing";
    preExistingPrs.value = [];
    commitsAhead.value = "2";
    pushFails.value = false;
    createFails.value = false;
    prCreated.value = false;
    calls.value = [];
  });

  // The regression: this branch is only reachable after the LLM skill ran and
  // its post-condition gate failed, so the stage already cost full LLM price.
  // Recording "deterministic" — which the schema defines as "skill skipped" —
  // booked that spend as a deterministic saving. One observed stage billed
  // $1.80 over 276s and was filed as a zero-cost deterministic run.
  it("records execution_path='llm' when the deterministic fallback rescues a failed LLM create", async () => {
    const { verify, paths } = makeOrchestratorForRecovery();

    await verify(42);

    const decision = paths().get("pr-create");
    expect(decision?.path).toBe("llm");
    expect(decision?.path).not.toBe("deterministic");
  });

  it("keeps the recovery observable via punt_reason rather than dropping it", async () => {
    const { verify, paths } = makeOrchestratorForRecovery();

    await verify(42);

    expect(paths().get("pr-create")?.puntReason).toMatch(/^deterministic-recovery:/);
  });
});

describe("tryDeterministicCreateFallback (#3927)", () => {
  beforeEach(() => {
    binaryPath.value = "/fake/nightgauge";
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
    expect(
      countCalls((c) => c[0] === "/fake/nightgauge" && c[1] === "pr" && c[2] === "create")
    ).toBe(1);
  });

  it("uses --body (not a heredoc) and includes the closing keyword + issue title", async () => {
    await callFallback();
    const create = calls.value.find(
      (c) => c[0] === "/fake/nightgauge" && c[1] === "pr" && c[2] === "create"
    );
    expect(create).toBeDefined();
    expect(create).toContain("--body");
    expect(create).toContain("--title");
    const body = create![create!.indexOf("--body") + 1];
    expect(body).toContain("Refs #42");
    expect(body).not.toMatch(/\bCloses #42\b/);
    const title = create![create!.indexOf("--title") + 1];
    expect(title).toBe("Add thing");
  });

  it("short-circuits to created:true without pushing when an open PR already exists", async () => {
    preExistingPrs.value = [{ number: 50 }];
    const result = await callFallback();
    expect(result.created).toBe(true);
    expect(countCalls((c) => c[0] === "git" && c[1] === "push")).toBe(0);
    expect(
      countCalls((c) => c[0] === "/fake/nightgauge" && c[1] === "pr" && c[2] === "create")
    ).toBe(0);
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
    expect(
      countCalls((c) => c[0] === "/fake/nightgauge" && c[1] === "pr" && c[2] === "create")
    ).toBe(0);
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
    expect(
      countCalls((c) => c[0] === "/fake/nightgauge" && c[1] === "pr" && c[2] === "create")
    ).toBe(0);
  });

  it("returns created:false with a reason when gh pr create fails", async () => {
    createFails.value = true;
    const result = await callFallback();
    expect(result.created).toBe(false);
    expect(result.reason).toMatch(/nightgauge pr create failed/i);
  });

  // #2506: the binary authors with the config identity and stamps the
  // pipeline marker from --issue/--run-id; ambient gh must never open it.
  it("opens the PR through the binary with --issue and --run-id, never gh pr create", async () => {
    const result = await callFallback();
    expect(result.created).toBe(true);
    expect(countCalls((c) => c[0] === "gh" && c[1] === "pr" && c[2] === "create")).toBe(0);
    const create = calls.value.find(
      (c) => c[0] === "/fake/nightgauge" && c[1] === "pr" && c[2] === "create"
    )!;
    expect(create[create.indexOf("--issue") + 1]).toBe("42");
    expect(create).toContain("--run-id");
    expect(create[create.indexOf("--repo") + 1]).toBe("o/r");
  });

  it("fails closed when the binary is unresolved", async () => {
    binaryPath.value = null;
    const result = await callFallback();
    expect(result.created).toBe(false);
    expect(result.reason).toMatch(/binary unresolved/i);
    expect(countCalls((c) => c[0] === "gh" && c[1] === "pr" && c[2] === "create")).toBe(0);
  });
});

function resetMocks() {
  stateDir.value = fs.mkdtempSync(path.join(os.tmpdir(), "pr-create-fallback-"));
  headSha.value = "abc123";
  binaryPath.value = "/fake/nightgauge";
  branchName.value = "feat/42-add-thing";
  preExistingPrs.value = [];
  commitsAhead.value = "2";
  pushFails.value = false;
  createFails.value = false;
  prCreated.value = false;
  calls.value = [];
}

function writeState(name: string, body: unknown) {
  fs.writeFileSync(path.join(stateDir.value, name), JSON.stringify(body));
}

const isPush = (c: string[]) => c[0] === "git" && c[1] === "push";
const isCreate = (c: string[]) => c[0] === "/fake/nightgauge" && c[1] === "pr" && c[2] === "create";

describe("post-create verification honours a pr-create gate stop (#2677)", () => {
  beforeEach(resetMocks);
  afterEach(() => fs.rmSync(stateDir.value, { recursive: true, force: true }));

  const recordName = "pr-create-gate-stop-42.json";

  it("opens no PR and fails with the gate's reason when the stop was recorded at HEAD", async () => {
    writeState(recordName, {
      gate: "scope_drift",
      reason: "1 file outside the docs allowlist: src/x.ts",
      head_sha: "abc123",
    });
    const { verify, paths } = makeOrchestratorForRecovery();

    const err = await verify(42);

    expect(err?.message).toContain("[pr-create-gate-stop]");
    expect(err?.message).toContain("scope_drift: 1 file outside the docs allowlist: src/x.ts");
    expect(countCalls(isPush)).toBe(0);
    expect(countCalls(isCreate)).toBe(0);
    expect(paths().get("pr-create")?.puntReason).toContain("blocking gate");
    expect(fs.existsSync(path.join(stateDir.value, recordName))).toBe(false);
  });

  it("discards a stale record from another HEAD and runs the fallback", async () => {
    writeState(recordName, { gate: "security_scan", reason: "secret", head_sha: "old999" });
    const { verify } = makeOrchestratorForRecovery();

    const err = await verify(42);

    expect(err).toBeNull();
    expect(countCalls(isPush)).toBe(1);
    expect(countCalls(isCreate)).toBe(1);
    expect(fs.existsSync(path.join(stateDir.value, recordName))).toBe(false);
  });

  it("runs the fallback unchanged when no record exists", async () => {
    const { verify } = makeOrchestratorForRecovery();

    expect(await verify(42)).toBeNull();
    expect(countCalls(isCreate)).toBe(1);
  });
});

describe("fallback closing keyword follows validate verdicts (#2677)", () => {
  beforeEach(resetMocks);
  afterEach(() => fs.rmSync(stateDir.value, { recursive: true, force: true }));

  async function fallbackBody(): Promise<string> {
    await callFallback();
    const create = calls.value.find(isCreate);
    expect(create).toBeDefined();
    return create![create!.indexOf("--body") + 1];
  }

  it("writes Closes #N when every criterion is met", async () => {
    writeState("validate-42.json", {
      acceptance_criteria_verdicts: [
        { index: 1, status: "met" },
        { index: 2, status: "met" },
      ],
    });
    const body = await fallbackBody();
    expect(body.startsWith("Closes #42")).toBe(true);
    expect(body).not.toMatch(/\bRefs #42\b/);
  });

  it.each([
    ["one criterion unmet", [{ status: "met" }, { status: "unmet" }]],
    ["one criterion not verifiable", [{ status: "met" }, { status: "not_verifiable" }]],
    ["no criteria", []],
  ])("writes Refs #N when %s", async (_label, verdicts) => {
    writeState("validate-42.json", { acceptance_criteria_verdicts: verdicts });
    const body = await fallbackBody();
    expect(body.startsWith("Refs #42")).toBe(true);
    expect(body).not.toMatch(/\bCloses #42\b/);
  });

  it("writes Refs #N when validate-{N}.json is missing", async () => {
    const body = await fallbackBody();
    expect(body.startsWith("Refs #42")).toBe(true);
  });
});
