/**
 * Pinned control task integrity (Issue #2117).
 *
 * The control is a real GitHub issue pinned to a frozen base commit. Its task
 * file carries a snapshot of the issue (`title` = issue title, `instruction` =
 * issue body, verbatim) and `source_issue.content_sha256`, the hash of that
 * snapshot plus the base SHA. Two checks use the hash:
 *   - `verifyRegisteredHash` (pure, offline) — run by `loadEvalTasks`, so a task
 *     file edited without re-registering fails at load time;
 *   - `verifyControlIssue` (network, injectable fetcher) — run by
 *     `scripts/evaluate-models.ts` before any cell is dispatched, so an edit to
 *     the live issue fails the run before any model spend.
 *
 * @see docs/decisions/011-model-eval-system.md
 * @see evals/tasks/README.md - "Control task"
 */

import { createHash } from "node:crypto";
import type { EvalTask } from "./modelEvalSchemas.js";

/** The issue text a control is hashed over. */
export interface ControlIssueContent {
  title: string;
  body: string;
  base_sha: string;
}

/** Fetches a GitHub issue's title and body by URL. */
export type IssueFetcher = (url: string) => Promise<{ title: string; body: string }>;

/**
 * sha256 hex of `JSON.stringify([title, body, base_sha])`, with CRLF in the body
 * normalized to LF (GitHub returns CRLF for bodies edited in the web UI). The
 * JSON array encoding has no separator collisions; nothing else is normalized.
 */
export function controlContentHash(content: ControlIssueContent): string {
  const body = content.body.replace(/\r\n/g, "\n");
  return createHash("sha256")
    .update(JSON.stringify([content.title, body, content.base_sha]))
    .digest("hex");
}

function requireSourceIssue(task: EvalTask): NonNullable<EvalTask["source_issue"]> {
  if (!task.source_issue) {
    throw new Error(`task "${task.id}" has no source_issue; it is not a control task`);
  }
  return task.source_issue;
}

/** Throws when the task file's own snapshot no longer matches its registered hash. */
export function verifyRegisteredHash(task: EvalTask): void {
  const src = requireSourceIssue(task);
  const actual = controlContentHash({
    title: task.title,
    body: task.instruction,
    base_sha: src.base_sha,
  });
  if (actual !== src.content_sha256) {
    throw new Error(
      `control task "${task.id}" drifted from its registered hash: registered ${src.content_sha256}, ` +
        `task file hashes to ${actual}. The title, instruction and base SHA are frozen; ` +
        `re-register the control (evals/tasks/README.md) instead of editing them.`
    );
  }
}

/**
 * Pre-dispatch drift check: hashes the live issue with the registered base SHA
 * and throws when it differs from `source_issue.content_sha256`. Returns the
 * verified hash.
 */
export async function verifyControlIssue(
  task: EvalTask,
  fetchIssue: IssueFetcher = ghIssueFetcher
): Promise<string> {
  const src = requireSourceIssue(task);
  const live = await fetchIssue(src.url);
  const actual = controlContentHash({ title: live.title, body: live.body, base_sha: src.base_sha });
  if (actual !== src.content_sha256) {
    throw new Error(
      `control issue ${src.url} drifted: registered hash ${src.content_sha256}, live hash ${actual}. ` +
        `The control issue must never be edited; restore it or re-register the control.`
    );
  }
  return actual;
}

/** Default fetcher: `gh issue view <url> --json title,body`. */
export const ghIssueFetcher: IssueFetcher = async (url) => {
  const { execFile } = await import("node:child_process");
  const stdout = await new Promise<string>((resolvePromise, reject) => {
    execFile("gh", ["issue", "view", url, "--json", "title,body"], (err, out, errOut) => {
      if (err) reject(new Error(`gh issue view ${url} failed: ${errOut.trim() || err.message}`));
      else resolvePromise(out);
    });
  });
  const parsed = JSON.parse(stdout) as { title?: unknown; body?: unknown };
  if (typeof parsed.title !== "string" || typeof parsed.body !== "string") {
    throw new Error(`gh issue view ${url} returned no title/body`);
  }
  return { title: parsed.title, body: parsed.body };
};
