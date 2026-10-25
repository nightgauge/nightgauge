/**
 * Move an issue to "In review" only when an open PR backs it (#1496).
 *
 * The extension's failure safety nets used to write "In review"
 * unconditionally. The Go scheduler's revert writes "Ready" for a PR-less
 * failure in the same second, nothing orders the two, and when the extension
 * landed last the issue sat in "In review" with no PR, rejected on every scan.
 *
 * Every extension write of "In review" (the failure safety nets and the
 * pr-create stage exit) now goes through `nightgauge project sync-status <N> in-review`,
 * which refuses unless an open PR whose head branch names the issue exists —
 * the same evidence the scheduler's own "In review" writes require. A refusal
 * is the expected outcome on a PR-less failure: nothing is written, nothing is
 * retried and no other status is substituted, so the scheduler's revert is the
 * only write and the board ends where its log line says.
 */

import { execFile } from "child_process";
import { promisify } from "util";
import { BinaryResolver } from "../services/BinaryResolver";
import type { Logger } from "./logger";

const execFileAsync = promisify(execFile);

/** The CLI's refusal text (`errInReviewWithoutOpenPR` in project_inreview_guard.go). */
export const IN_REVIEW_REFUSAL = "refusing In review";

export type ParkInReviewOutcome = "moved" | "refused" | "failed";

export interface ParkInReviewDeps {
  resolveBinary: () => Promise<string | null>;
  exec: (binary: string, args: string[], cwd: string) => Promise<unknown>;
}

const defaultDeps: ParkInReviewDeps = {
  resolveBinary: () => BinaryResolver.fromVSCode().resolve(),
  exec: (binary, args, cwd) => execFileAsync(binary, args, { cwd, timeout: 60000 }),
};

export async function moveToInReviewIfPRBacked(
  issueNumber: number,
  cwd: string,
  logger: Logger,
  repoNameWithOwner?: string,
  deps: ParkInReviewDeps = defaultDeps
): Promise<ParkInReviewOutcome> {
  const binary = await deps.resolveBinary();
  if (!binary) {
    logger.warn("Not moving issue to In review: nightgauge binary not found", {
      issueNumber,
    });
    return "failed";
  }
  const args = ["project", "sync-status", String(issueNumber), "in-review"];
  if (repoNameWithOwner) {
    args.push("--repo", repoNameWithOwner);
  }
  try {
    await deps.exec(binary, args, cwd);
    logger.info("Moved issue to In review: its open PR awaits review", { issueNumber });
    return "moved";
  } catch (err) {
    const e = err as { stderr?: unknown; message?: unknown };
    const text = `${String(e?.stderr ?? "")}\n${String(e?.message ?? err)}`;
    if (text.includes(IN_REVIEW_REFUSAL)) {
      logger.info("Issue has no open PR: not moved to In review (status left for the scheduler)", {
        issueNumber,
      });
      return "refused";
    }
    logger.warn("Failed to move issue to In review", { issueNumber, err: text.trim() });
    return "failed";
  }
}
