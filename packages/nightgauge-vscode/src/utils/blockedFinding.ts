/**
 * The durable out-of-scope blocked finding (#1147).
 *
 * #1142 gave a stage that discovers "this cannot be done within this issue's
 * scope" an honest terminal — the run books the first-class `blocked` outcome
 * instead of an anonymous validation failure. What it did NOT do is write the
 * discovery down, so the next dispatch of the same issue spent planning, dev
 * and validate rediscovering the identical wall. This module is the durable
 * half: the finding the stage produced, persisted where the NEXT run's pickup
 * can read it before spending a token.
 *
 * WHERE IT LIVES, AND WHY NOT SOMEWHERE THAT ALREADY EXISTS:
 *
 *  - **Not `.nightgauge/retros/`.** A retro is a dated post-mortem
 *    (`YYYY-MM-DD_<issue>_retro.json`) read by the escalated-retry path for
 *    *diagnostic* context. This is a single-valued, standing per-issue FACT
 *    that has to be looked up by issue number and cleared when it stops being
 *    true — a date-keyed append-only report answers neither question.
 *  - **Not the knowledge base.** `.nightgauge/knowledge/` is per-feature
 *    Markdown that graduates into `docs/`; the extension has no write path to
 *    it at all (writes are skill-side `nightgauge knowledge` verbs). This is
 *    machine state a scheduler reads, not reader-facing prose.
 *  - **Not `.nightgauge/attention/`.** The decision-request store is the
 *    daemon's to write, and its records are the *presentation* of a condition
 *    (dedup, journal, lifecycle). The finding is the condition itself, and it
 *    must be readable with no daemon running.
 *  - **Not flat under `.nightgauge/pipeline/`.** That is where the per-run
 *    context files live, and `runstate.ArchiveRun` moves EVERY `*-<issue>.json`
 *    directly under `pipeline/` into `pipeline/history/<runId>/` when the run
 *    ends. A finding written as `blocked-1147.json` would be archived out of
 *    the way by the very run that produced it, which is precisely the
 *    "discarded context file" #1147 exists to stop. `ArchiveRun` skips
 *    directories, so the finding lives in a subdirectory of the same tree.
 *
 * Hence `<repoRoot>/.nightgauge/pipeline/blocked-findings/<issue>.json`, rooted
 * at the RUN'S repo root (`getRunRepoRoot()`) rather than the worktree — a
 * finding written into a worktree dies with it at cleanup.
 *
 * @see Issue #1147
 * @see Issue #1142 — the `blocked` fork this makes durable
 */

import * as fs from "fs";
import * as path from "path";
import type { Logger } from "./logger";

/** Directory name under `.nightgauge/pipeline/`. A DIRECTORY on purpose — see the module doc. */
export const BLOCKED_FINDINGS_DIRNAME = "blocked-findings";

/**
 * A stage's "this needs work outside this issue" discovery, persisted.
 *
 * Every field is copied verbatim from the deliverable's `feedback[]` signal —
 * this record re-states what the stage said, and classifies nothing of its own.
 * `evidence` in particular is preserved as written: it is the array that
 * carries the `blocked-on:` / `blocked-by:` / `external-blocker:` /
 * `out-of-scope:` marker, and it is what a human reads to decide which real
 * `blockedBy` edges to create. Nothing here parses it — see the #1147 PR body
 * for why automatic edge creation was rejected.
 */
export interface BlockedFinding {
  schema_version: "1.0";
  issue_number: number;
  /** The stage whose deliverable carried the signal. */
  stage: string;
  /** The signal's `signal_type` (e.g. PLAN_REVISION_NEEDED). */
  signal_type: string;
  /** Why the signal was not answered with a rewind — `notRewindableReason`. */
  reason: string;
  /** The signal's `rationale`, verbatim. */
  rationale: string;
  /** The signal's `evidence`, verbatim. */
  evidence: string[];
  /** The run that recorded it, for the audit trail. Empty when unresolvable. */
  run_id: string;
  recorded_at: string;
}

/** `<root>/.nightgauge/pipeline/blocked-findings` */
export function blockedFindingsDir(root: string): string {
  return path.join(root, ".nightgauge", "pipeline", BLOCKED_FINDINGS_DIRNAME);
}

/** `<root>/.nightgauge/pipeline/blocked-findings/<issue>.json` */
export function blockedFindingPath(root: string, issueNumber: number): string {
  return path.join(blockedFindingsDir(root), `${issueNumber}.json`);
}

/**
 * Persist a finding, overwriting any earlier one for the same issue.
 *
 * Last-write-wins is deliberate: the condition is "issue N is blocked on work
 * outside its scope", which is single-valued. A second run that reaches the
 * same wall for a different reason should leave the CURRENT reason on disk, not
 * a pile of stale ones a reader has to date-sort.
 *
 * Returns whether the write landed. Never throws: a bookkeeping write must not
 * turn a terminal classification into a crash.
 */
export function writeBlockedFinding(
  root: string,
  finding: BlockedFinding,
  logger?: Logger
): boolean {
  try {
    fs.mkdirSync(blockedFindingsDir(root), { recursive: true });
    fs.writeFileSync(
      blockedFindingPath(root, finding.issue_number),
      JSON.stringify(finding, null, 2),
      "utf-8"
    );
    return true;
  } catch (err) {
    logger?.warn("Failed to persist the out-of-scope blocked finding (#1147)", {
      issueNumber: finding.issue_number,
      err: err instanceof Error ? err.message : String(err),
    });
    return false;
  }
}

/**
 * Read the finding for an issue, or null when there is none.
 *
 * A malformed file reads as null rather than throwing, and that is the safe
 * direction: this is consulted at pickup to DEFER a run, so an unreadable
 * finding must let the run proceed (it will simply rediscover the wall) rather
 * than wedge the issue on a parse error nobody can see.
 */
export function readBlockedFinding(root: string, issueNumber: number): BlockedFinding | null {
  const file = blockedFindingPath(root, issueNumber);
  try {
    if (!fs.existsSync(file)) {
      return null;
    }
    const parsed = JSON.parse(fs.readFileSync(file, "utf-8")) as Partial<BlockedFinding>;
    // Read POSITIVELY, never "it parsed, so it must be one".
    //
    // A hit here defers a run, so the cost of accepting a file that is not a
    // finding is an issue that silently stops running. Three things must hold:
    // the record's own schema version, a non-empty `signal_type` (which only
    // this writer produces — a pipeline context file has neither), and an
    // `issue_number` matching the file's name. The last is the same stance
    // readCurrentRunId takes on a mismatched run-state.json — an honest "no
    // finding" beats a confident wrong one.
    if (parsed.schema_version !== "1.0") {
      return null;
    }
    if (typeof parsed.signal_type !== "string" || parsed.signal_type.trim() === "") {
      return null;
    }
    if (typeof parsed.issue_number !== "number" || parsed.issue_number !== issueNumber) {
      return null;
    }
    return {
      schema_version: "1.0",
      issue_number: issueNumber,
      stage: String(parsed.stage ?? ""),
      signal_type: String(parsed.signal_type ?? ""),
      reason: String(parsed.reason ?? ""),
      rationale: String(parsed.rationale ?? ""),
      evidence: Array.isArray(parsed.evidence) ? parsed.evidence.map(String) : [],
      run_id: String(parsed.run_id ?? ""),
      recorded_at: String(parsed.recorded_at ?? ""),
    };
  } catch {
    return null;
  }
}

/**
 * One-line summary for the deferral's operator-facing lines, mirroring the
 * `blockerList` the open-blockedBy deferral renders.
 */
export function describeBlockedFinding(finding: BlockedFinding): string {
  const marker = finding.evidence.find((e) =>
    /^\s*(blocked-on|blocked-by|external-blocker|out-of-scope)\s*:/i.test(e)
  );
  return marker?.trim() || finding.rationale || finding.signal_type;
}
