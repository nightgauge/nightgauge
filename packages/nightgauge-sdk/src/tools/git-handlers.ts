/**
 * Git Operation Tool Handlers
 *
 * Server-side handlers for structured git diff, log, and status operations.
 * These handlers execute when Claude's Python code invokes git tools via PTC.
 *
 * @see Issue #1070 - Optimize context file and git batch operations
 * @see packages/nightgauge-sdk/src/tools/definitions/git.ts
 */

import { execFileSync } from "child_process";
import type { ToolHandler, ToolResult } from "./tool-handlers.js";

/**
 * Run git with an explicit argument vector and capture output.
 * Returns structured result regardless of exit code.
 *
 * Uses execFileSync (no shell) rather than execSync. These handlers take refs
 * and paths straight from a model-supplied tool schema; interpolating them into
 * a shell string would let `$(...)`, backticks, `;` or `|` execute — quoting is
 * not sufficient, since command substitution runs inside double quotes. With an
 * argv array there is no shell, so those characters are inert data.
 */
function runGitCommand(
  args: string[],
  cwd: string
): { exitCode: number; stdout: string; stderr: string } {
  try {
    const stdout = execFileSync("git", args, {
      cwd,
      encoding: "utf-8",
      timeout: 30_000, // 30s max for git commands
      stdio: ["pipe", "pipe", "pipe"],
      maxBuffer: 10 * 1024 * 1024,
    });
    return { exitCode: 0, stdout: stdout ?? "", stderr: "" };
  } catch (error: unknown) {
    const execError = error as {
      status?: number;
      stdout?: string;
      stderr?: string;
    };
    return {
      exitCode: execError.status ?? 1,
      stdout: (execError.stdout as string) ?? "",
      stderr: (execError.stderr as string) ?? "",
    };
  }
}

/** Handler for `git_diff_summary` tool */
export class GitDiffSummaryHandler implements ToolHandler {
  readonly name = "git_diff_summary";

  async execute(input: Record<string, unknown>, cwd: string): Promise<ToolResult> {
    const base = typeof input.base === "string" ? input.base : "HEAD~1";
    const head = typeof input.head === "string" ? input.head : "HEAD";
    const stagedOnly = input.staged_only === true;

    const args = stagedOnly ? ["diff", "--cached", "--numstat"] : ["diff", "--numstat", base, head];

    const result = runGitCommand(args, cwd);

    if (result.exitCode !== 0) {
      return {
        success: false,
        output: {
          success: false,
          error: result.stderr || "git diff failed",
        },
      };
    }

    const entries: Array<{
      file: string;
      insertions: number;
      deletions: number;
    }> = [];
    let totalInsertions = 0;
    let totalDeletions = 0;

    const lines = result.stdout.trim().split("\n").filter(Boolean);
    for (const line of lines) {
      const parts = line.split("\t");
      if (parts.length >= 3) {
        const ins = parts[0] === "-" ? 0 : parseInt(parts[0], 10);
        const del = parts[1] === "-" ? 0 : parseInt(parts[1], 10);
        const file = parts[2];
        entries.push({ file, insertions: ins, deletions: del });
        totalInsertions += ins;
        totalDeletions += del;
      }
    }

    return {
      success: true,
      output: {
        success: true,
        files_changed: entries.length,
        insertions: totalInsertions,
        deletions: totalDeletions,
        entries,
      },
    };
  }
}

/** Handler for `git_log_structured` tool */
export class GitLogStructuredHandler implements ToolHandler {
  readonly name = "git_log_structured";

  async execute(input: Record<string, unknown>, cwd: string): Promise<ToolResult> {
    const count = typeof input.count === "number" && input.count > 0 ? Math.floor(input.count) : 10;
    const since = typeof input.since === "string" ? input.since : undefined;
    const path = typeof input.path === "string" ? input.path : undefined;

    // Use a delimiter unlikely to appear in commit messages
    const SEP = "<<<SEP>>>";
    const format = `${SEP}%H${SEP}%h${SEP}%s${SEP}%an${SEP}%aI`;
    // No shell: pass each flag as one argv entry and do NOT wrap values in
    // quotes — a shell would strip them, execFileSync would pass them through
    // literally and git would treat them as part of the value.
    const args = ["log", "-n", String(count), `--format=${format}`];
    if (since) {
      args.push(`--since=${since}`);
    }
    if (path) {
      args.push("--", path);
    }

    const result = runGitCommand(args, cwd);

    if (result.exitCode !== 0) {
      return {
        success: false,
        output: {
          success: false,
          error: result.stderr || "git log failed",
        },
      };
    }

    const commits: Array<{
      sha: string;
      short_sha: string;
      message: string;
      author: string;
      date: string;
    }> = [];

    const rawEntries = result.stdout.split(SEP).filter((s) => s.trim().length > 0);

    // Each commit produces 5 fields after splitting
    for (let i = 0; i + 4 < rawEntries.length; i += 5) {
      commits.push({
        sha: rawEntries[i].trim(),
        short_sha: rawEntries[i + 1].trim(),
        message: rawEntries[i + 2].trim(),
        author: rawEntries[i + 3].trim(),
        date: rawEntries[i + 4].trim(),
      });
    }

    return {
      success: true,
      output: {
        success: true,
        commits,
        total: commits.length,
      },
    };
  }
}

/** Handler for `git_status_structured` tool */
export class GitStatusStructuredHandler implements ToolHandler {
  readonly name = "git_status_structured";

  async execute(_input: Record<string, unknown>, cwd: string): Promise<ToolResult> {
    const statusResult = runGitCommand(["status", "--porcelain"], cwd);
    const branchResult = runGitCommand(["branch", "--show-current"], cwd);

    if (statusResult.exitCode !== 0) {
      return {
        success: false,
        output: {
          success: false,
          error: statusResult.stderr || "git status failed",
        },
      };
    }

    const branch = branchResult.stdout.trim();
    const staged: Array<{ file: string; status: string }> = [];
    const unstaged: Array<{ file: string; status: string }> = [];
    const untracked: string[] = [];

    const lines = statusResult.stdout.trim().split("\n").filter(Boolean);
    for (const line of lines) {
      const indexStatus = line[0];
      const workTreeStatus = line[1];
      const file = line.slice(3);

      if (indexStatus === "?" && workTreeStatus === "?") {
        untracked.push(file);
      } else {
        if (indexStatus !== " " && indexStatus !== "?") {
          staged.push({
            file,
            status: this.statusLabel(indexStatus),
          });
        }
        if (workTreeStatus !== " " && workTreeStatus !== "?") {
          unstaged.push({
            file,
            status: this.statusLabel(workTreeStatus),
          });
        }
      }
    }

    return {
      success: true,
      output: {
        success: true,
        branch,
        staged,
        unstaged,
        untracked,
        is_clean: staged.length === 0 && unstaged.length === 0 && untracked.length === 0,
      },
    };
  }

  private statusLabel(code: string): string {
    const labels: Record<string, string> = {
      M: "modified",
      A: "added",
      D: "deleted",
      R: "renamed",
      C: "copied",
      U: "unmerged",
    };
    return labels[code] ?? "unknown";
  }
}

/**
 * Create the git operation tool handler map.
 * Maps tool names to their server-side handler implementations.
 */
export function createGitHandlers(): Map<string, ToolHandler> {
  const handlers = new Map<string, ToolHandler>();
  const instances: ToolHandler[] = [
    new GitDiffSummaryHandler(),
    new GitLogStructuredHandler(),
    new GitStatusStructuredHandler(),
  ];
  for (const handler of instances) {
    handlers.set(handler.name, handler);
  }
  return handlers;
}
