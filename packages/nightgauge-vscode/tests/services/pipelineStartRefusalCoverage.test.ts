/**
 * #1170 — EVERY zero-token `pipeline-start` refusal must make a deliberate
 * choice about the two durable reactions (board move to "In review", public
 * failure comment), and the choice is expressed by the typed `startRefusal`
 * union on the returned `PipelineRunResult`.
 *
 * This pins the enumeration itself. The bug was not that one refusal reacted
 * wrongly — it was that the manager reacted to a whole CLASS of refusals it had
 * never been asked about. A future `pipeline-start` early-return added without
 * a `startRefusal` would silently rejoin that class: it would move the board and
 * comment by default, which is the exact failure mode, and no behavioural test
 * would notice because nobody would have written one for a return site that did
 * not exist yet.
 *
 * So the guard is on the SOURCE: any object literal in HeadlessOrchestrator.ts
 * carrying `failedStage: "pipeline-start"` must also carry `startRefusal`.
 * Deciding it does NOT qualify is still a decision — omit the field and this
 * test fails until the omission is argued for here.
 */

import { describe, it, expect } from "vitest";
import { readFileSync } from "fs";
import { join } from "path";

const SOURCE = join(__dirname, "..", "..", "src", "services", "HeadlessOrchestrator.ts");

/**
 * The refusals enumerated in #1170, in source order. Kept as data so the
 * failure message names what changed rather than just a count.
 */
const EXPECTED_REFUSALS = [
  "epic-with-open-sub-issues",
  "issue-closed",
  "github-quota-low",
  "github-network-outage",
  "github-auth-failed",
  "adapter-auth-failed",
  "budget-cancelled-by-user",
] as const;

describe("pipeline-start refusal coverage (#1170)", () => {
  const source = readFileSync(SOURCE, "utf8");
  const lines = source.split("\n");
  const refusalSites = lines
    .map((line, index) => ({ line: line.trim(), index }))
    .filter(({ line }) => line === 'failedStage: "pipeline-start",');

  it("finds the pipeline-start return sites at all (a premise check)", () => {
    // If this drops to zero the assertions below pass vacuously — a scan-based
    // guard that finds nothing proves nothing.
    expect(refusalSites.length).toBeGreaterThan(0);
  });

  it("stamps every pipeline-start refusal with a typed startRefusal", () => {
    const unstamped: number[] = [];
    for (const site of refusalSites) {
      // Look within the same object literal — the following few lines, up to
      // the literal's closing brace.
      const window = lines.slice(site.index, site.index + 12);
      const end = window.findIndex((l) => /^\s*\};?\s*$/.test(l));
      const body = (end === -1 ? window : window.slice(0, end + 1)).join("\n");
      if (!/\bstartRefusal:\s*"/.test(body)) {
        unstamped.push(site.index + 1);
      }
    }
    expect(
      unstamped,
      `HeadlessOrchestrator.ts has pipeline-start refusals with no startRefusal at line(s) ` +
        `${unstamped.join(", ")}. A zero-token refusal that omits it moves the board to ` +
        `"In review" and comments on the issue (#1170). Add the kind to the PipelineStartRefusal ` +
        `union, or argue here why this refusal is a genuine failure that deserves both.`
    ).toEqual([]);
  });

  it("stamps exactly the refusals #1170 enumerated", () => {
    const stamped = [...source.matchAll(/\bstartRefusal:\s*"([a-z-]+)"/g)].map((m) => m[1]);
    expect(stamped).toEqual([...EXPECTED_REFUSALS]);
  });
});
