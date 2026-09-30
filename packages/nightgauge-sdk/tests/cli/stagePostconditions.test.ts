import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import * as fs from "node:fs/promises";
import * as path from "node:path";
import * as os from "node:os";
import { execFileSync, spawn } from "node:child_process";
import { validateCodexStagePostconditions } from "../../src/cli/commands/stage.js";
import { initCloneRepo } from "../../src/__tests__/helpers/gitRepo.js";

// Mock spawn to handle git commands without requiring a real git repo
vi.mock("node:child_process", async (importOriginal) => {
  const actual = (await importOriginal()) as typeof import("node:child_process");
  const { Readable } = await import("node:stream");

  return {
    ...actual,
    spawn: vi.fn((cmd: string, args: string[], options: any) => {
      // Create a mock child process
      if (cmd === "git" && args[0] === "branch" && args[1] === "--show-current") {
        // Return 'master' as the current branch
        const mockStdout = new Readable();
        mockStdout.push("master\n");
        mockStdout.push(null);

        const mockStderr = new Readable();
        mockStderr.push(null);

        const mockProcess = {
          stdout: mockStdout,
          stderr: mockStderr,
          on: vi.fn((event: string, callback: (code: number) => void) => {
            if (event === "close") {
              setTimeout(() => callback(0), 0);
            }
          }),
          once: vi.fn(),
          removeListener: vi.fn(),
          kill: vi.fn(),
          pid: 12345,
        } as any;

        return mockProcess;
      }

      // Fall back to actual spawn for other commands
      return actual.spawn(cmd, args, options);
    }),
  };
});

describe("validateCodexStagePostconditions", () => {
  let tempRoot: string;
  let pipelineDir: string;
  const issueNumber = 614;

  beforeEach(async () => {
    tempRoot = await fs.mkdtemp(path.join(os.tmpdir(), "stage-post-"));
    // A real repository: context files resolve through its git common dir
    // (ADR-024 § 7). The branch queries (spawn) stay mocked.
    pipelineDir = initCloneRepo(tempRoot).pipeline;
    await fs.mkdir(pipelineDir, { recursive: true });

    await fs.writeFile(path.join(tempRoot, "README.md"), "test\n", "utf-8");
  });

  afterEach(async () => {
    await fs.rm(tempRoot, { recursive: true, force: true });
  });

  it("fails when required stage output context file is missing", async () => {
    await expect(
      validateCodexStagePostconditions({
        stage: "feature-planning",
        issueNumber,
        cwd: tempRoot,
      })
    ).rejects.toThrow(/required context file is missing/);
  });

  it("fails issue-pickup when branch mismatch exists", async () => {
    await fs.writeFile(
      path.join(pipelineDir, `issue-${issueNumber}.json`),
      JSON.stringify({ branch: "feat/614-test" }),
      "utf-8"
    );

    await expect(
      validateCodexStagePostconditions({
        stage: "issue-pickup",
        issueNumber,
        cwd: tempRoot,
      })
    ).rejects.toThrow(/protected branch|does not match issue context branch/);
  });

  it("fails pr-merge when cleanup did not remove context files", async () => {
    await fs.writeFile(path.join(pipelineDir, `pr-${issueNumber}.json`), "{}", "utf-8");

    await expect(
      validateCodexStagePostconditions({
        stage: "pr-merge",
        issueNumber,
        cwd: tempRoot,
      })
    ).rejects.toThrow(/context cleanup is incomplete/);
  });
});
