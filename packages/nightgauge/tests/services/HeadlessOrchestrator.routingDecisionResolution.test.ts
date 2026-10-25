/**
 * #1489: routed skip_stages must reach the autonomous path. issue-pickup writes
 * issue-N.json from the run's worktree; the orchestrator may read from either
 * the worktree or the main checkout. Both resolve through pipelineStateDir, so
 * these tests pin that with a real linked worktree rather than a stubbed path.
 */
import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, realpathSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import * as path from "node:path";
import { afterEach, describe, expect, it, vi } from "vitest";

import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import { pipelineStateDir } from "../../src/utils/cloneLayout";

const ISSUE = 4242;
const SKIP = ["feature-planning", "feature-validate"];

function git(cwd: string, ...args: string[]): void {
  execFileSync("git", args, { cwd, stdio: "ignore" });
}

function makeRepoWithWorktree(): { base: string; mainRoot: string; worktree: string } {
  const base = realpathSync(mkdtempSync(path.join(tmpdir(), "routing-1489-")));
  const mainRoot = path.join(base, "main");
  const worktree = path.join(base, "wt");
  mkdirSync(mainRoot);
  git(mainRoot, "init", "-q", "-b", "main");
  git(mainRoot, "config", "user.email", "test@example.com");
  git(mainRoot, "config", "user.name", "test");
  git(mainRoot, "commit", "-q", "--allow-empty", "-m", "init");
  git(mainRoot, "worktree", "add", "-q", "-b", `feat/${ISSUE}-x`, worktree);
  return { base, mainRoot, worktree };
}

function writeIssueContext(dir: string): void {
  mkdirSync(dir, { recursive: true });
  writeFileSync(
    path.join(dir, `issue-${ISSUE}.json`),
    JSON.stringify({
      schema_version: "1.3",
      issue_number: ISSUE,
      labels: ["size:XS", "type:bug"],
      routing: {
        change_type: "code",
        task_type: "bug",
        complexity_score: 1,
        suggested_route: "trivial",
        skip_stages: SKIP,
        rationale: "trivial",
        estimated_time_minutes: 5,
      },
    })
  );
}

function orchestratorAt(root: string) {
  const log = { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn() };
  const orchestrator = new HeadlessOrchestrator(null, log as any) as any;
  orchestrator.getWorkingDirectory = () => root;
  return { orchestrator, log };
}

describe("loadRoutingDecision resolution (#1489)", () => {
  const bases: string[] = [];
  afterEach(() => {
    for (const b of bases.splice(0)) rmSync(b, { recursive: true, force: true });
  });

  it("worktree and main checkout share one pipeline state directory", () => {
    const { base, mainRoot, worktree } = makeRepoWithWorktree();
    bases.push(base);
    expect(pipelineStateDir(worktree)).toBe(pipelineStateDir(mainRoot));
  });

  for (const which of ["worktree", "mainRoot"] as const) {
    it(`applies routed skip_stages when the orchestrator root is the ${which}`, async () => {
      const repo = makeRepoWithWorktree();
      bases.push(repo.base);
      // Written where issue-pickup writes: from the worktree.
      writeIssueContext(pipelineStateDir(repo.worktree));

      const { orchestrator } = orchestratorAt(repo[which]);
      const decision = await orchestrator.loadRoutingDecision(ISSUE);
      expect(decision).not.toBeNull();
      expect(decision.skipStages).toEqual(SKIP);

      orchestrator.currentRoutingDecision = decision;
      expect(orchestrator.shouldSkipStage("feature-planning")).toBe(true);
      expect(orchestrator.shouldSkipStage("feature-validate")).toBe(true);
      expect(orchestrator.shouldSkipStage("feature-dev")).toBe(false);
    });
  }

  it("logs a missing issue context at info with the resolved path", async () => {
    const { base, worktree } = makeRepoWithWorktree();
    bases.push(base);
    const { orchestrator, log } = orchestratorAt(worktree);

    expect(await orchestrator.loadRoutingDecision(ISSUE)).toBeNull();
    expect(log.info).toHaveBeenCalledWith(expect.stringContaining("Issue context file not found"), {
      issueNumber: ISSUE,
      issueContextPath: path.join(pipelineStateDir(worktree), `issue-${ISSUE}.json`),
    });
    expect(log.debug).not.toHaveBeenCalledWith(
      expect.stringContaining("Issue context file not found"),
      expect.anything()
    );
  });
});
