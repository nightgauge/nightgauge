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
import { classifyTerminalKind } from "@nightgauge/sdk";

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
  { text: "API Error: Overloaded", kind: "api_overloaded", branch: "529 overload" },
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
    ]) {
      expect(source).toContain(`"${kind}"`);
    }
    expect(source).toContain("HALT_SKIP_ENVIRONMENTAL");
    expect(source).toContain("HALT_SKIP_TRANSIENT_STALL");
    expect(source).toContain("const haltKind = classifyTerminalKind(haltErrMsg);");
  });

  it("keeps exactly one deliberate raw-text condition, and says why", () => {
    // A bare Anthropic "session/usage limit" with no model named is a shape the
    // TAXONOMY does not classify — Go returns "" for it — so this branch cannot
    // be expressed as a kind. It is an addition to the table's answer, not a
    // duplicate of it, and it must stay documented rather than quietly grow
    // back into a ladder.
    const rawTextTests = source.match(/\/[^/\n]*\/i\.test\(haltErrMsg\)/g) ?? [];
    expect(
      rawTextTests,
      "a new raw-text matcher appeared in haltQueueOnSlotFailure — resolve the kind through the " +
        "canonical table instead, or document the addition here"
    ).toEqual(["/\\b(?:session|usage)\\s+limit\\b/i.test(haltErrMsg)"]);
    expect(classifyTerminalKind("You've hit your usage limit · resets 10am")).toBeUndefined();
  });
});
