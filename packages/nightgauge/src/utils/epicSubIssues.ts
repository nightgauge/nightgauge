/**
 * Read an issue's whole sub-issue list through the nightgauge binary (#1701).
 *
 * `nightgauge issue list --epic <N> --json` reads the issue's `subIssues`
 * connection page by page to its end, and exits non-zero when it cannot: a
 * connection still reporting a next page after the binary's page cap, or a
 * later page that fails, is an error, never a short list. A fixed
 * `subIssues(first: 50)` read stopped at the first page with no sign that
 * more existed, so every sub-issue past it was silently left out.
 */

import { execFile } from "child_process";
import { promisify } from "util";

/** One sub-issue, as the binary reports it. */
export interface EpicSubIssue {
  number: number;
  /** GitHub's issue state: `OPEN` or `CLOSED`. */
  state: string;
  /** The sub-issue's own repository, `owner/name`; it may differ from the epic's. */
  repo: string;
}

/** The options this module passes to the subprocess. */
export interface EpicSubIssueExecOptions {
  cwd?: string;
  timeout?: number;
  env?: NodeJS.ProcessEnv;
}

/** Runs a file with arguments and resolves with its stdout; rejects on a non-zero exit. */
export type RunFile = (
  file: string,
  args: string[],
  options: EpicSubIssueExecOptions
) => Promise<{ stdout: string }>;

const execFileAsync = promisify(execFile);

const runFile: RunFile = async (file, args, options) => {
  const { stdout } = await execFileAsync(file, args, { ...options, encoding: "utf-8" });
  return { stdout };
};

/**
 * Read every sub-issue of `issueNumber` in `repoNameWithOwner`.
 *
 * An issue with no sub-issues yields an empty list. A failed read, output that
 * is not the binary's epic-progress JSON, or a list shorter than the count the
 * binary reports, throws: the caller never receives part of the list as if it
 * were the whole.
 *
 * @param binary - Absolute path of the nightgauge binary.
 * @param repoNameWithOwner - The issue's repository, `owner/name`.
 * @param issueNumber - The issue whose sub-issues to read.
 * @param options - cwd, timeout and env for the subprocess.
 * @param run - The subprocess runner; tests replace it.
 */
export async function readEpicSubIssues(
  binary: string,
  repoNameWithOwner: string,
  issueNumber: number,
  options: EpicSubIssueExecOptions,
  run: RunFile = runFile
): Promise<EpicSubIssue[]> {
  const what = `sub-issues of ${repoNameWithOwner}#${issueNumber}`;
  const { stdout } = await run(
    binary,
    ["issue", "list", "--epic", String(issueNumber), "--repo", repoNameWithOwner, "--json"],
    options
  );

  let parsed: unknown;
  try {
    parsed = JSON.parse(stdout);
  } catch (err) {
    throw new Error(
      `${what}: nightgauge issue list printed invalid JSON: ` +
        (err instanceof Error ? err.message : String(err)),
      { cause: err }
    );
  }
  if (parsed === null || typeof parsed !== "object") {
    throw new Error(`${what}: nightgauge issue list printed no epic-progress object`);
  }

  const { total, subIssues } = parsed as { total?: unknown; subIssues?: unknown };
  if (typeof total !== "number" || !Number.isInteger(total) || total < 0) {
    throw new Error(`${what}: nightgauge issue list reported no sub-issue total`);
  }
  // The binary omits the list (null) when there are no sub-issues.
  const nodes = subIssues ?? [];
  if (!Array.isArray(nodes)) {
    throw new Error(`${what}: nightgauge issue list reported subIssues that are not a list`);
  }
  if (nodes.length !== total) {
    throw new Error(
      `${what}: nightgauge issue list returned ${nodes.length} sub-issues but counted ${total}`
    );
  }

  return nodes.map((node: unknown, i: number) => {
    const n = node as { number?: unknown; state?: unknown; repo?: unknown } | null;
    if (
      !n ||
      typeof n.number !== "number" ||
      !Number.isInteger(n.number) ||
      typeof n.state !== "string" ||
      typeof n.repo !== "string"
    ) {
      throw new Error(`${what}: sub-issue ${i} has no number, state or repository`);
    }
    return { number: n.number, state: n.state, repo: n.repo };
  });
}
