/**
 * Git Operation Tool Handler Tests
 *
 * Tests for GitDiffSummaryHandler, GitLogStructuredHandler,
 * and GitStatusStructuredHandler.
 * child_process is mocked at module level so no real git commands run.
 *
 * @see Issue #1070 - Optimize context file and git batch operations
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("child_process", () => ({
  execFileSync: vi.fn(),
}));

import { execFileSync } from "child_process";
import {
  GitDiffSummaryHandler,
  GitLogStructuredHandler,
  GitStatusStructuredHandler,
  createGitHandlers,
} from "../../src/tools/git-handlers.js";

const mockExecSync = vi.mocked(execFileSync);
const CWD = "/project";

/** Helper: make execFileSync return stdout as if the command succeeded. */
function mockSuccess(stdout: string): void {
  mockExecSync.mockReturnValueOnce(stdout as unknown as Buffer);
}

/** Helper: make execFileSync throw as if the command failed. */
function mockFailure(opts: { status?: number; stdout?: string; stderr?: string }): void {
  const err = Object.assign(new Error("command failed"), {
    status: opts.status ?? 1,
    stdout: opts.stdout ?? "",
    stderr: opts.stderr ?? "",
  });
  mockExecSync.mockImplementationOnce(() => {
    throw err;
  });
}

/** The argv array passed to git on the Nth (0-based) call. */
function argvOf(callIndex = 0): string[] {
  return mockExecSync.mock.calls[callIndex]?.[1] as unknown as string[];
}

// ---------------------------------------------------------------------------
// GitDiffSummaryHandler
// ---------------------------------------------------------------------------

describe("GitDiffSummaryHandler", () => {
  let handler: GitDiffSummaryHandler;

  beforeEach(() => {
    handler = new GitDiffSummaryHandler();
    vi.clearAllMocks();
  });

  it('has name "git_diff_summary"', () => {
    expect(handler.name).toBe("git_diff_summary");
  });

  it("parses numstat output into structured entries", async () => {
    mockSuccess("10\t5\tsrc/foo.ts\n3\t1\tsrc/bar.ts\n");

    const result = await handler.execute({}, CWD);

    expect(result.success).toBe(true);
    expect(result.output["files_changed"]).toBe(2);
    expect(result.output["insertions"]).toBe(13);
    expect(result.output["deletions"]).toBe(6);
    const entries = result.output["entries"] as Array<Record<string, unknown>>;
    expect(entries).toHaveLength(2);
    expect(entries[0]).toEqual({
      file: "src/foo.ts",
      insertions: 10,
      deletions: 5,
    });
  });

  it("handles empty diff (no changes)", async () => {
    mockSuccess("");

    const result = await handler.execute({}, CWD);

    expect(result.success).toBe(true);
    expect(result.output["files_changed"]).toBe(0);
    expect(result.output["insertions"]).toBe(0);
    expect(result.output["deletions"]).toBe(0);
    expect(result.output["entries"]).toEqual([]);
  });

  it("uses base and head parameters", async () => {
    mockSuccess("");

    await handler.execute({ base: "main", head: "feat/123" }, CWD);

    expect(mockExecSync).toHaveBeenCalledWith(
      "git",
      ["diff", "--numstat", "main", "feat/123"],
      expect.objectContaining({ cwd: CWD })
    );
  });

  it("defaults base to HEAD~1 and head to HEAD", async () => {
    mockSuccess("");

    await handler.execute({}, CWD);

    expect(mockExecSync).toHaveBeenCalledWith(
      "git",
      ["diff", "--numstat", "HEAD~1", "HEAD"],
      expect.any(Object)
    );
  });

  it("uses --cached flag for staged_only", async () => {
    mockSuccess("");

    await handler.execute({ staged_only: true }, CWD);

    expect(mockExecSync).toHaveBeenCalledWith(
      "git",
      ["diff", "--cached", "--numstat"],
      expect.any(Object)
    );
  });

  it("returns error on git failure", async () => {
    mockFailure({ stderr: "fatal: bad revision" });

    const result = await handler.execute({}, CWD);

    expect(result.success).toBe(false);
    expect(result.output["error"]).toContain("bad revision");
  });

  it("handles binary files (- in numstat)", async () => {
    mockSuccess("-\t-\timage.png\n5\t2\tsrc/main.ts\n");

    const result = await handler.execute({}, CWD);

    expect(result.success).toBe(true);
    const entries = result.output["entries"] as Array<Record<string, unknown>>;
    expect(entries[0]).toEqual({
      file: "image.png",
      insertions: 0,
      deletions: 0,
    });
    expect(entries[1]).toEqual({
      file: "src/main.ts",
      insertions: 5,
      deletions: 2,
    });
  });
});

// ---------------------------------------------------------------------------
// GitLogStructuredHandler
// ---------------------------------------------------------------------------

describe("GitLogStructuredHandler", () => {
  let handler: GitLogStructuredHandler;
  const SEP = "<<<SEP>>>";

  beforeEach(() => {
    handler = new GitLogStructuredHandler();
    vi.clearAllMocks();
  });

  it('has name "git_log_structured"', () => {
    expect(handler.name).toBe("git_log_structured");
  });

  it("parses structured log output", async () => {
    const logOutput = `${SEP}abc123full${SEP}abc1234${SEP}feat: add feature${SEP}John${SEP}2026-02-20T10:00:00+00:00\n`;
    mockSuccess(logOutput);

    const result = await handler.execute({}, CWD);

    expect(result.success).toBe(true);
    const commits = result.output["commits"] as Array<Record<string, unknown>>;
    expect(commits).toHaveLength(1);
    expect(commits[0]["sha"]).toBe("abc123full");
    expect(commits[0]["short_sha"]).toBe("abc1234");
    expect(commits[0]["message"]).toBe("feat: add feature");
    expect(commits[0]["author"]).toBe("John");
    expect(result.output["total"]).toBe(1);
  });

  it("respects count parameter", async () => {
    mockSuccess("");

    await handler.execute({ count: 5 }, CWD);

    expect(argvOf()).toEqual(expect.arrayContaining(["-n", "5"]));
  });

  it("defaults count to 10", async () => {
    mockSuccess("");

    await handler.execute({}, CWD);

    expect(argvOf()).toEqual(expect.arrayContaining(["-n", "10"]));
  });

  it("includes --since when provided", async () => {
    mockSuccess("");

    await handler.execute({ since: "2 days ago" }, CWD);

    // No shell → the value must NOT be quoted; git receives it as one argv entry.
    expect(argvOf()).toContain("--since=2 days ago");
  });

  it("includes path filter when provided", async () => {
    mockSuccess("");

    await handler.execute({ path: "src/" }, CWD);

    const argv = argvOf();
    expect(argv.slice(-2)).toEqual(["--", "src/"]);
  });

  it("returns error on git failure", async () => {
    mockFailure({ stderr: "fatal: not a git repository" });

    const result = await handler.execute({}, CWD);

    expect(result.success).toBe(false);
    expect(result.output["error"]).toContain("not a git repository");
  });

  it("handles empty log (no commits)", async () => {
    mockSuccess("");

    const result = await handler.execute({}, CWD);

    expect(result.success).toBe(true);
    expect(result.output["commits"]).toEqual([]);
    expect(result.output["total"]).toBe(0);
  });

  it("floors non-integer count values", async () => {
    mockSuccess("");

    await handler.execute({ count: 3.7 }, CWD);

    expect(argvOf()).toEqual(expect.arrayContaining(["-n", "3"]));
  });

  // Regression: these handlers take refs/paths straight from a model-supplied
  // tool schema. With execSync they were interpolated into a `/bin/sh -c`
  // string, where `$(...)` executes even inside double quotes. With execFileSync
  // there is no shell, so the metacharacters must survive as inert literal argv.
  it("passes shell metacharacters through as inert literal argv (no shell)", async () => {
    mockSuccess("");
    const handler = new GitLogStructuredHandler();

    await handler.execute({ since: '"; touch /tmp/pwned; #', path: "$(id > /tmp/pwned)" }, CWD);

    const [file, argv] = mockExecSync.mock.calls[0] as unknown as [string, string[]];
    expect(file).toBe("git");
    expect(argv).toContain('--since="; touch /tmp/pwned; #');
    expect(argv.slice(-2)).toEqual(["--", "$(id > /tmp/pwned)"]);
  });

  it("passes injection-shaped refs through as literal argv in git_diff_summary", async () => {
    mockSuccess("");
    const diff = new GitDiffSummaryHandler();

    await diff.execute({ base: "main; rm -rf /", head: "`whoami`" }, CWD);

    expect(mockExecSync).toHaveBeenCalledWith(
      "git",
      ["diff", "--numstat", "main; rm -rf /", "`whoami`"],
      expect.any(Object)
    );
  });
});

// ---------------------------------------------------------------------------
// GitStatusStructuredHandler
// ---------------------------------------------------------------------------

describe("GitStatusStructuredHandler", () => {
  let handler: GitStatusStructuredHandler;

  beforeEach(() => {
    handler = new GitStatusStructuredHandler();
    vi.clearAllMocks();
  });

  it('has name "git_status_structured"', () => {
    expect(handler.name).toBe("git_status_structured");
  });

  it("parses porcelain output with staged, unstaged, and untracked files", async () => {
    // Status result
    mockSuccess("M  src/modified.ts\n?? new-file.ts\n A src/added.ts\n");
    // Branch result
    mockSuccess("feat/1070-test\n");

    const result = await handler.execute({}, CWD);

    expect(result.success).toBe(true);
    expect(result.output["branch"]).toBe("feat/1070-test");
    expect(result.output["is_clean"]).toBe(false);

    const staged = result.output["staged"] as Array<Record<string, unknown>>;
    expect(staged).toHaveLength(1);
    expect(staged[0]["status"]).toBe("modified");

    const untracked = result.output["untracked"] as string[];
    expect(untracked).toContain("new-file.ts");
  });

  it("detects clean working tree", async () => {
    mockSuccess(""); // empty status
    mockSuccess("main\n"); // branch

    const result = await handler.execute({}, CWD);

    expect(result.success).toBe(true);
    expect(result.output["is_clean"]).toBe(true);
    expect(result.output["staged"]).toEqual([]);
    expect(result.output["unstaged"]).toEqual([]);
    expect(result.output["untracked"]).toEqual([]);
  });

  it("identifies current branch", async () => {
    mockSuccess("");
    mockSuccess("feat/awesome-feature\n");

    const result = await handler.execute({}, CWD);

    expect(result.output["branch"]).toBe("feat/awesome-feature");
  });

  it("returns error on git status failure", async () => {
    mockFailure({ stderr: "fatal: not a git repository" });

    const result = await handler.execute({}, CWD);

    expect(result.success).toBe(false);
    expect(result.output["error"]).toContain("not a git repository");
  });

  it("handles added files in staging area", async () => {
    mockSuccess("A  src/new.ts\n");
    mockSuccess("main\n");

    const result = await handler.execute({}, CWD);

    const staged = result.output["staged"] as Array<Record<string, unknown>>;
    expect(staged).toHaveLength(1);
    expect(staged[0]["status"]).toBe("added");
  });

  it("handles deleted files", async () => {
    mockSuccess("D  src/old.ts\n");
    mockSuccess("main\n");

    const result = await handler.execute({}, CWD);

    const staged = result.output["staged"] as Array<Record<string, unknown>>;
    expect(staged).toHaveLength(1);
    expect(staged[0]["status"]).toBe("deleted");
  });

  it("handles files with both staged and unstaged changes", async () => {
    mockSuccess("MM src/both.ts\n");
    mockSuccess("main\n");

    const result = await handler.execute({}, CWD);

    const staged = result.output["staged"] as Array<Record<string, unknown>>;
    const unstaged = result.output["unstaged"] as Array<Record<string, unknown>>;
    expect(staged).toHaveLength(1);
    expect(unstaged).toHaveLength(1);
  });
});

// ---------------------------------------------------------------------------
// createGitHandlers()
// ---------------------------------------------------------------------------

describe("createGitHandlers()", () => {
  it("returns a Map", () => {
    const handlers = createGitHandlers();
    expect(handlers).toBeInstanceOf(Map);
  });

  it("contains exactly 3 handlers", () => {
    const handlers = createGitHandlers();
    expect(handlers.size).toBe(3);
  });

  it("contains git_diff_summary handler", () => {
    const handlers = createGitHandlers();
    expect(handlers.has("git_diff_summary")).toBe(true);
    expect(handlers.get("git_diff_summary")).toBeInstanceOf(GitDiffSummaryHandler);
  });

  it("contains git_log_structured handler", () => {
    const handlers = createGitHandlers();
    expect(handlers.has("git_log_structured")).toBe(true);
    expect(handlers.get("git_log_structured")).toBeInstanceOf(GitLogStructuredHandler);
  });

  it("contains git_status_structured handler", () => {
    const handlers = createGitHandlers();
    expect(handlers.has("git_status_structured")).toBe(true);
    expect(handlers.get("git_status_structured")).toBeInstanceOf(GitStatusStructuredHandler);
  });

  it("returns a new Map on each call", () => {
    const a = createGitHandlers();
    const b = createGitHandlers();
    expect(a).not.toBe(b);
  });

  it("each handler name matches its map key", () => {
    const handlers = createGitHandlers();
    for (const [key, handler] of handlers) {
      expect(handler.name).toBe(key);
    }
  });
});
