/**
 * #1169 — the Go layer's classification and the extension's reaction must not
 * diverge.
 *
 * THIS IS THE ROOT CAUSE, PINNED. The bug was not that someone forgot a string.
 * It was that two layers hold two halves of one decision with nothing between
 * them: Go's `onPipelineComplete` decides a terminal kind is retryable infra and
 * says so in as many words — "no lifetime-cap increment, no cascade feed, no
 * pause" — and the extension's `haltQueueOnSlotFailure` independently decides
 * whether to clear the queue and call `autonomousPause()`. When `adapter_auth_failed`
 * was added to the Go side (#312) and not to `HALT_SKIP_ENVIRONMENTAL`, the
 * extension silently overrode a decision the Go layer had already made, and an
 * operator who restarted autonomous with a logged-out CLI watched the fleet stop
 * five times over. `api_overloaded` (#3835) was the same divergence, one kind
 * earlier; its branch comment describes this exact failure.
 *
 * `concurrentPipelineManager.haltPolicy.test.ts` pins the extension's policy
 * against the canonical kind table. It cannot catch this class of bug, because
 * both halves of a divergence are internally consistent. This suite reads the
 * GO SOURCE and asserts the two halves agree.
 *
 * It is a source read rather than a running Go call because the two artifacts
 * are in different languages and different processes; there is no runtime at
 * which both decisions are observable at once. What it reads is not prose: the
 * `terminalFailureKind == TerminalKind*` branch structure and the constant
 * table are the actual control flow, and `guardIsNotVacuous` below fails if
 * either stops parsing — so this cannot rot into a test that checks nothing.
 */

import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import path from "node:path";

const REPO_ROOT = path.resolve(__dirname, "../../../..");
const AUTONOMOUS_GO = path.join(REPO_ROOT, "internal/orchestrator/autonomous.go");
const FAILURE_HANDLER_GO = path.join(REPO_ROOT, "internal/orchestrator/failure_handler.go");
const CPM_TS = path.resolve(__dirname, "../../src/services/ConcurrentPipelineManager.ts");

/** `TerminalKindAdapterAuthFailed` → `adapter_auth_failed`, from Go's own table. */
function goKindConstants(): Map<string, string> {
  const src = readFileSync(FAILURE_HANDLER_GO, "utf-8");
  const out = new Map<string, string>();
  for (const m of src.matchAll(/\b(TerminalKind\w+)\s*=\s*"([a-z0-9_]+)"/g)) {
    out.set(m[1], m[2]);
  }
  return out;
}

interface GoBranch {
  /** Kind strings the branch condition tests for. */
  kinds: string[];
  /** True when the branch's own logging declares it does not pause the queue. */
  declaresNoPause: boolean;
}

/**
 * Every `if terminalFailureKind == TerminalKind*` branch in autonomous.go's
 * completion handler, with the kinds it covers and whether its body says it
 * does not pause.
 */
function goTerminalKindBranches(): GoBranch[] {
  const src = readFileSync(AUTONOMOUS_GO, "utf-8");
  const consts = goKindConstants();
  const branches: GoBranch[] = [];

  const IF = "if terminalFailureKind ==";
  for (let at = src.indexOf(IF); at !== -1; at = src.indexOf(IF, at + IF.length)) {
    const open = src.indexOf("{", at);
    if (open === -1) continue;
    const condition = src.slice(at, open);
    const kinds: string[] = [];
    for (const m of condition.matchAll(/\bTerminalKind\w+/g)) {
      const kind = consts.get(m[0]);
      if (kind) kinds.push(kind);
    }
    // Brace-balance the branch body.
    let depth = 0;
    let end = open;
    for (let i = open; i < src.length; i++) {
      if (src[i] === "{") depth++;
      else if (src[i] === "}" && --depth === 0) {
        end = i;
        break;
      }
    }
    const body = src.slice(open, end + 1);
    branches.push({ kinds, declaresNoPause: /no pause/.test(body) });
  }
  return branches;
}

/** The kinds the extension's halt policy declines to halt on. */
function extensionSkipKinds(): Set<string> {
  const src = readFileSync(CPM_TS, "utf-8")
    // Comments quote Go's log lines verbatim; strip them so the vocabulary
    // measured is the executable policy, not the commentary about it.
    .replace(/\/\*[\s\S]*?\*\//g, "")
    .replace(/\/\/[^\n]*/g, "");

  const kinds = new Set<string>();
  for (const setName of ["HALT_SKIP_ENVIRONMENTAL", "HALT_SKIP_TRANSIENT_STALL"]) {
    const at = src.indexOf(setName);
    expect(at, `${setName} is gone from ConcurrentPipelineManager`).toBeGreaterThan(0);
    const open = src.indexOf("new Set([", at);
    const close = src.indexOf("]", open);
    expect(close, `${setName} literal did not parse`).toBeGreaterThan(open);
    for (const m of src.slice(open, close).matchAll(/"([a-z0-9_]+)"/g)) kinds.add(m[1]);
  }
  // The two kinds handled by named branches rather than by a set: the 529
  // overload and the transport drop. Both are skip decisions of the same policy.
  for (const m of src.matchAll(/haltKind === "([a-z0-9_]+)"/g)) kinds.add(m[1]);
  for (const m of src.matchAll(/kind === "([a-z0-9_]+)"/g)) kinds.add(m[1]);
  return kinds;
}

describe("Go/extension halt-decision parity (#1169)", () => {
  it("parses both layers — the guard is not vacuous", () => {
    // If either parse silently returns nothing, every assertion below passes
    // over an empty set and this suite becomes decoration. Fail here instead.
    const consts = goKindConstants();
    expect(
      consts.size,
      `no TerminalKind constants parsed from ${FAILURE_HANDLER_GO}`
    ).toBeGreaterThan(10);
    expect(consts.get("TerminalKindAdapterAuthFailed")).toBe("adapter_auth_failed");

    const branches = goTerminalKindBranches();
    expect(
      branches.length,
      `no terminalFailureKind branches parsed from ${AUTONOMOUS_GO}`
    ).toBeGreaterThan(10);
    expect(
      branches.some((b) => b.declaresNoPause),
      "no branch parsed as no-pause"
    ).toBe(true);
    expect(
      branches.some((b) => !b.declaresNoPause),
      "every branch parsed as no-pause — the /no pause/ test is matching everything"
    ).toBe(true);

    expect(extensionSkipKinds().size).toBeGreaterThan(3);
  });

  it("does not halt the queue for a kind Go routes as retryable infra with no pause", () => {
    // THE REGRESSION. Go's adapter_auth_failed branch logs "retryable infra,
    // retry in %v (no lifetime-cap increment, no cascade feed, no pause)" and
    // autonomous_adapter_auth_test.go pins that a burst leaves the scheduler
    // running. Both halves are asserted, so this fails whichever side moves:
    // Go dropping the no-pause routing, or the extension dropping the skip.
    const goBranches = goTerminalKindBranches();
    const goNoPause = new Set(goBranches.filter((b) => b.declaresNoPause).flatMap((b) => b.kinds));

    expect(
      goNoPause.has("adapter_auth_failed"),
      "Go no longer routes adapter_auth_failed as a no-pause kind — if that is deliberate, " +
        "remove it from HALT_SKIP_ENVIRONMENTAL in the same change"
    ).toBe(true);

    expect(
      extensionSkipKinds().has("adapter_auth_failed"),
      "adapter_auth_failed is back to halting the queue while Go says no pause — this is #1169, " +
        "and before it #3835 (api_overloaded). Add it to HALT_SKIP_ENVIRONMENTAL."
    ).toBe(true);
  });

  it("forces a decision on every kind Go declares no-pause, instead of letting one slip", () => {
    // #1169 was not a typo. It was a kind acquiring a no-pause routing on the Go
    // side (#312) with nothing that made the extension answer for it. The kind
    // then sat in the gap for months, and only an operator watching five repos
    // stop found it.
    //
    // So the reconciliation is total: every kind Go's own branch declares
    // no-pause for is either mirrored in the extension's skip policy, or listed
    // below with a reason. Adding a no-pause kind on the Go side now fails this
    // test until somebody writes down which it is. Nothing here is optional and
    // nothing defaults to "fine".
    const NOT_MIRRORED: Record<string, string> = {
      // Never reaches haltQueueOnSlotFailure as a kind: the extension answers a
      // blocked terminal earlier and by a typed flag
      // (pipelineResult.blocked.outOfScopeFinding), because the OTHER producer
      // of `blocked` — #190's pr-merge dead end — is a repo-config fault a human
      // must clear and DOES still halt. See that branch's comment.
      blocked_dependency:
        "handled before the kind is consulted, via the typed blocked/outOfScopeFinding flag",
      // Go retries the harness's tool-call denial (#289) on a short backoff.
      // The extension takes no position: a run that keeps being denied is a
      // stage asking for something it should not, which is a defect a human
      // should see rather than a blip to ride out.
      permission_denied:
        "deliberately still halts here — a repeated harness denial is a defect, not a blip",
      // Same shape: Go retries in case the model comes back, but a model that
      // is gone for this workspace is a configuration answer, not weather.
      model_unavailable:
        "deliberately still halts here — an unavailable model is a config decision, not weather",
    };

    const goBranches = goTerminalKindBranches();
    const goNoPause = [
      ...new Set(goBranches.filter((b) => b.declaresNoPause).flatMap((b) => b.kinds)),
    ];
    const skipped = extensionSkipKinds();

    const unanswered = goNoPause.filter((k) => !skipped.has(k) && !(k in NOT_MIRRORED)).sort();
    expect(
      unanswered,
      "Go declares these kinds no-pause and the extension neither skips the halt for them nor " +
        "records why it does not. This is the #1169 gap. Add each to HALT_SKIP_ENVIRONMENTAL " +
        "or to NOT_MIRRORED with a reason."
    ).toEqual([]);

    // And the exclusions list cannot go stale: an entry that IS mirrored, or
    // that Go no longer routes no-pause, is a note about code that moved.
    const stale = Object.keys(NOT_MIRRORED)
      .filter((k) => skipped.has(k) || !goNoPause.includes(k))
      .sort();
    expect(stale, "NOT_MIRRORED describes kinds that have since changed — re-read it").toEqual([]);

    // The reconciliation covers real ground on both sides.
    expect(goNoPause.length).toBeGreaterThan(4);
    expect(goNoPause.filter((k) => skipped.has(k)).length).toBeGreaterThan(2);
  });
});
