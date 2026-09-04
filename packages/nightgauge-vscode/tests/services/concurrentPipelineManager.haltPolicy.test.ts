/**
 * concurrentPipelineManager.haltPolicy.test.ts
 *
 * The queue-halt policy is a KIND-SET decision, not a text ladder (#306).
 *
 * `haltQueueOnSlotFailure` used to carry four private ladders of regexes —
 * isTransientNetworkFailureText, isEnvironmentalFailure, isApiOverloaded,
 * isStallKill — each with a "Match strings mirror Go's ClassifyTerminalKind —
 * keep aligned" comment and nothing checking the claim. They were the fourth,
 * fifth, sixth and seventh copies of terminal-kind matching in the codebase, and
 * the manifest's own note admitted they could still drift.
 *
 * They now resolve the kind through the canonical table and test membership, so
 * what is left in that file is only POLICY: which kinds skip the halt. This
 * suite pins the policy the way the corpus pins classification — against real
 * failure text, with the kind coming from the table.
 *
 * `haltQueueOnSlotFailure` is a private method on a class with a large
 * constructor surface, so rather than instantiating it this suite asserts the
 * decision the policy is made of: kind ∈ set. If the source stops asking the
 * table for the kind, the parity fence over that file (#257) and the greps
 * below fail.
 */

import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import path from "node:path";
import { classifyTerminalKind, signalTerminalKind } from "@nightgauge/sdk";

const CPM_PATH = path.resolve(__dirname, "../../src/services/ConcurrentPipelineManager.ts");
const source = readFileSync(CPM_PATH, "utf-8");

/** The failure texts each skip-branch exists for, as producers actually emit them. */
const SKIP_CASES: { text: string; kind: string; branch: string }[] = [
  {
    text: "API Error: The socket connection was closed unexpectedly",
    kind: "api_connection_lost",
    branch: "transient network blip",
  },
  {
    text: "[pipeline-start-failure] github-network-outage: api.github.com unreachable",
    kind: "github_network_outage",
    branch: "transient network blip",
  },
  {
    text: "API Error: Stream idle timeout - partial response received",
    kind: "stream_idle_timeout",
    branch: "environmental",
  },
  {
    text: "Stage [rate-limit-quota-exhausted] idle 2m 14s after rate_limit_event",
    kind: "rate_limit_quota_exhausted",
    branch: "environmental",
  },
  {
    text: "network unavailable: extended github connectivity loss",
    kind: "network_unavailable",
    branch: "environmental",
  },
  {
    // #1169 — the pipeline-start auth gate's own wording, verbatim from
    // HeadlessOrchestrator. Go routes this kind as retryable infra with an
    // explicit "no pause"; this layer must not override that.
    text:
      "[adapter-auth-failed] Auth pre-flight failed — adapter not authenticated. " +
      "Pipeline halted before AI stages (zero tokens spent).",
    kind: "adapter_auth_failed",
    branch: "environmental",
  },
  { text: "API Error: Overloaded", kind: "api_overloaded", branch: "529 overload" },
  {
    // #1391 — GitHub's own secondary-limit wording, verbatim from `gh`. Go
    // routes this kind with the transient-infra branch and says "no pause" in
    // as many words; this layer must not override that. Before the kind
    // existed the text classified as subagent_crash and therefore HALTED the
    // queue on a throttle that clears in minutes.
    text:
      "exit 1: gh: You have exceeded a secondary rate limit for the GitHub API\n" +
      "Please wait a few minutes before you try again",
    kind: "github_rate_limited",
    branch: "environmental",
  },
  {
    text: "[stall-killed] stage exceeded stall idle threshold",
    kind: "stall_kill",
    branch: "transient stall",
  },
  {
    text: "[runaway-ceiling-exceeded] runaway cost ceiling exceeded",
    kind: "stall_kill",
    branch: "transient stall",
  },
  {
    text: "ARCHITECTURE APPROVAL REQUIRED: a human must approve this decision",
    kind: "architecture_approval_required",
    branch: "approval pause",
  },
  // WIDENED vs the pre-#306 regexes, deliberately. main's isStallKill matched
  // `[stall-killed]`/`stall idle threshold`/`exceeded stage_hard_cap` and none
  // of these, so a zombie-run guard halted the queue; the table routes them
  // to stall_kill, which is the recovery they already got from the Go side.
  // (A second zombie-run fixture sat here until #470 retired its table clause —
  // #427 had already deleted that marker's only producer.)
  {
    text: "[stage-no-output-timeout] feature-dev produced no session output",
    kind: "stall_kill",
    branch: "transient stall (widened: #252 first-output watchdog)",
  },
  {
    text: "exit 1: stage stopped after the hard cap",
    kind: "stall_kill",
    branch: "transient stall (widened: bare `hard cap`, main required `exceeded stage_hard_cap`)",
  },
];

/** Failures that MUST reach the halt — the bugs the halt exists to surface. */
const HALT_CASES: { text: string; kind: string }[] = [
  { text: "exit 1: schema validation failed for plan.json", kind: "validation_error" },
  { text: "exit 137: killed by signal SIGKILL", kind: "subagent_crash" },
  {
    text: "[validation-failed] feature-validate wrote validation_status=failed",
    kind: "validation_failed",
  },
  { text: "[commit-orphaned] HEAD is not on the expected feature branch", kind: "commit_orphaned" },
  // NARROWED vs the pre-#306 regexes, deliberately. main's isStallKill fired on
  // any text containing stall wording, so a cost-cap kill that also mentioned
  // the stall skipped the halt with a toast. The table gives cost-cap-exceeded
  // precedence, budget_exceeded is in neither skip set, and a run that
  // deliberately spent its cap SHOULD stop the queue rather than be retried
  // into spending it again. No producer composes this shape today
  // (SkillRunner composes cost-cap text without stall wording), which is why it
  // is listed here rather than fixed.
  {
    text: "[cost-cap-exceeded] stage feature-dev [stall-killed] terminated",
    kind: "budget_exceeded",
  },
];

describe("queue-halt policy (ConcurrentPipelineManager)", () => {
  it("routes every skip-branch's real failure text to the kind that branch tests for", () => {
    for (const c of SKIP_CASES) {
      expect(classifyTerminalKind(c.text), `${c.branch}: ${c.text}`).toBe(c.kind);
    }
  });

  it("leaves real defects classified as kinds no skip-branch names", () => {
    const skipKinds = new Set(SKIP_CASES.map((c) => c.kind));
    for (const c of HALT_CASES) {
      expect(classifyTerminalKind(c.text), c.text).toBe(c.kind);
      expect(skipKinds.has(c.kind), `${c.kind} must reach the halt`).toBe(false);
    }
  });

  it("declares each skip-branch's kinds as a set, not as a matcher ladder", () => {
    // The point of #306 in this file: no branch may go back to deriving a
    // terminal kind from raw text. `classifyTerminalKind` is the only sanctioned
    // route, and these two sets are the only local vocabulary.
    for (const kind of [
      "stream_idle_timeout",
      "rate_limit_quota_exhausted",
      "network_unavailable",
      "adapter_auth_failed",
      "github_rate_limited",
    ]) {
      expect(source).toContain(`"${kind}"`);
    }
    expect(source).toContain("HALT_SKIP_ENVIRONMENTAL");
    expect(source).toContain("HALT_SKIP_TRANSIENT_STALL");
    expect(source).toContain("const haltKind = classifyTerminalKind(haltErrMsg);");
  });

  it("keeps exactly one deliberate raw-text condition, in any form, and says why", () => {
    // A bare Anthropic "session/usage limit" with no model named is a shape the
    // RECORD does not classify — Go returns "" for it — so the halt branch
    // cannot be expressed as a kind. It is an addition to the table's answer,
    // not a duplicate of it.
    //
    // The previous version of this guard grepped for the regex FORM
    // (`/…/i.test(haltErrMsg)`) and therefore passed for
    // `haltErrMsg.toLowerCase().includes("[baseline-ci]")` — executed, green.
    // So it now scans the whole method body for BOTH: every regex literal, and
    // every method called on the raw text.
    const body = methodBody(source, "private async haltQueueOnSlotFailure(");

    // String and template literals are blanked first: a log message containing
    // a slash otherwise reads as a regex and makes this assertion noise.
    const scannable = body.replace(/"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'|`(?:[^`\\]|\\.)*`/g, '""');
    // Branches are mutually exclusive (escape starts with \, class starts with
    // [, the rest excludes /, \, [ and newline) so the scan is linear — the
    // overlapping form flagged as js/redos backtracked exponentially on
    // bracket-heavy source.
    const regexes =
      scannable.match(/\/(?![/*])(?:\\.|\[(?:\\.|[^\]\\\n])*\]|[^/\\[\n])+\/[gimsuy]*/g) ?? [];
    expect(
      regexes,
      "a new regex appeared in haltQueueOnSlotFailure — resolve the kind through the canonical " +
        "table instead, or document the addition here"
    ).toEqual(["/\\b(?:session|usage)\\s+limit\\b/i"]);

    const textOps = [...body.matchAll(/haltErrMsg\s*\.\s*(\w+)/g)].map((m) => m[1]);
    expect(
      [...new Set(textOps)].sort(),
      "haltQueueOnSlotFailure called a string method on the raw failure text other than slice(). " +
        "Any of includes/match/test/startsWith/toLowerCase is a matcher the table cannot see."
    ).toEqual(["slice"]);

    // And the two halves of the story stay tied: the RECORD says nothing for
    // this wording, while the REACTION does — via the declared signal
    // extension, which is why the halt policy is not the only thing keeping it
    // alive any more.
    expect(classifyTerminalKind("You've hit your usage limit · resets 10am")).toBeUndefined();
    expect(signalTerminalKind("You've hit your usage limit · resets 10am")).toBe(
      "rate_limit_quota_exhausted"
    );
  });
});

/** The brace-balanced body of a method, comments stripped. */
function methodBody(src: string, decl: string): string {
  const start = src.indexOf(decl);
  expect(start, `${decl} is gone — this guard now checks nothing`).toBeGreaterThan(0);
  let depth = 0;
  for (let i = src.indexOf("{", start); i < src.length; i++) {
    if (src[i] === "{") depth++;
    else if (src[i] === "}" && --depth === 0) {
      return src
        .slice(start, i + 1)
        .replace(/\/\*[\s\S]*?\*\//g, "")
        .replace(/\/\/[^\n]*/g, "");
    }
  }
  throw new Error(`${decl} body is unbalanced`);
}
