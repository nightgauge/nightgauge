/**
 * stageActivity — pure activity model for the pipeline tree (#1897).
 *
 * The replay fixture is shaped after run 01a0bea2 (#1644): pr-merge pushed
 * four CI-fix commits, failed its post-condition gate, had a recovery action
 * declined and escalated sonnet → opus. None of it reached the tree.
 */

import { describe, it, expect } from "vitest";
import {
  formatStageActivityLabel,
  fromRuntimeState,
  mergeEvents,
  parseToolCall,
  summarize,
  type StageActivityEvent,
} from "../../../src/views/items/stageActivity";

const T0 = Date.parse("2026-10-01T10:00:00.000Z");
const at = (minutes: number) => new Date(T0 + minutes * 60_000).toISOString();
const IDLE_MS = 600_000;

const COMMITS: Array<[string, string]> = [
  ["ecd8e01b", "fix lint in pr-merge gate"],
  ["a19b5ff7", "restore missing test fixture"],
  ["f97f1d8c", "pin go toolchain for CI"],
  ["fea8ce8d", "resolve address-less sends against their fd"],
];

function commitResult(sha: string, headline: string): string {
  return `[feat/1644-sends ${sha}] ${headline}\n 2 files changed, 14 insertions(+), 3 deletions(-)\n`;
}

const CHECKS_RESULT = [
  "lint\tfail\t1m2s\thttps://github.com/x/y/actions/runs/1",
  "test\tpass\t3m10s\thttps://github.com/x/y/actions/runs/2",
  "build\tpending\t0\thttps://github.com/x/y/actions/runs/3",
].join("\n");

const RUNTIME_STATE = {
  stage_gate_results: {
    "pr-merge": [
      { gate_name: "pr-merge", passed: false, reason: "PR not MERGED", timestamp: at(30) },
    ],
  },
  stage_recovery_attempts: {
    "pr-merge": [
      {
        action: "skill-exited-without-merging",
        recovered: false,
        reason: "checks still failing",
        at: at(31),
      },
    ],
  },
  escalation_history: [
    { stage: "pr-merge", fromModel: "sonnet", toModel: "opus", reason: "gate_failed", at: at(32) },
    { stage: "feature-dev", fromModel: "haiku", toModel: "sonnet", reason: "other", at: at(1) },
  ],
};

describe("parseToolCall (heuristic)", () => {
  it("reads git's [branch sha] subject line", () => {
    const events = parseToolCall({
      result: commitResult("fea8ce8d", "resolve sends"),
      timestamp: at(1),
    });
    expect(events).toEqual([
      { kind: "fix-commit", at: at(1), sha: "fea8ce8d", headline: "resolve sends" },
    ]);
  });

  it("reads failing rows from a checks listing and nothing else", () => {
    const events = parseToolCall({ result: CHECKS_RESULT, timestamp: at(2) });
    expect(events).toEqual([{ kind: "check-failing", at: at(2), checks: ["lint"] }]);
  });

  it("ignores unrelated output and empty results", () => {
    expect(
      parseToolCall({ result: "On branch main\nnothing to commit", timestamp: at(1) })
    ).toEqual([]);
    expect(parseToolCall({ timestamp: at(1) })).toEqual([]);
  });
});

describe("fromRuntimeState", () => {
  it("returns only this stage's failed gates, recoveries and escalations", () => {
    const events = fromRuntimeState("pr-merge", RUNTIME_STATE);
    expect(events.map((e) => e.kind)).toEqual(["gate-failed", "recovery", "escalation"]);
  });

  it("drops passing gates", () => {
    const events = fromRuntimeState("pr-merge", {
      stage_gate_results: {
        "pr-merge": [{ gate_name: "pr-merge", passed: true, reason: "ok", timestamp: at(1) }],
      },
    });
    expect(events).toEqual([]);
  });
});

describe("replay of run 01a0bea2", () => {
  it("records every commit, the gate failure and the escalation, in order, with the state sequence", () => {
    let toolEvents: StageActivityEvent[] = [];
    const rounds: number[] = [];
    const states: string[] = [];
    const stageStartedAt = at(0);

    toolEvents = mergeEvents(
      toolEvents,
      parseToolCall({ result: CHECKS_RESULT, timestamp: at(2) })
    );
    COMMITS.forEach(([sha, headline], i) => {
      toolEvents = mergeEvents(
        toolEvents,
        parseToolCall({ result: commitResult(sha, headline), timestamp: at(5 + i * 5) })
      );
      const s = summarize(toolEvents, {
        now: Date.parse(at(6 + i * 5)),
        stageStartedAt,
        idleThresholdMs: IDLE_MS,
      });
      rounds.push(s.round);
      states.push(s.state);
    });
    expect(rounds).toEqual([1, 2, 3, 4]);

    const all = fromRuntimeState("pr-merge", RUNTIME_STATE);
    const afterGate = mergeEvents(all.slice(0, 2), toolEvents);
    states.push(
      summarize(afterGate, { now: Date.parse(at(31)), stageStartedAt, idleThresholdMs: IDLE_MS })
        .state
    );
    const final = summarize(mergeEvents(all, toolEvents), {
      now: Date.parse(at(33)),
      stageStartedAt,
      idleThresholdMs: IDLE_MS,
    });
    states.push(final.state);

    expect([...new Set(states)]).toEqual(["remediating", "retrying-after-gate", "escalated"]);
    expect(final.events.map((e) => (e.kind === "fix-commit" ? e.sha : e.kind))).toEqual([
      "check-failing",
      "ecd8e01b",
      "a19b5ff7",
      "f97f1d8c",
      "fea8ce8d",
      "gate-failed",
      "recovery",
      "escalation",
    ]);
    expect(formatStageActivityLabel(final)).toBe("escalated sonnet → opus");
    // The replayed stage is busy, so it never reads as idle.
    expect(final.state).not.toBe("idle");
  });

  it("labels the remediating and retrying states", () => {
    const tool = mergeEvents(
      parseToolCall({ result: CHECKS_RESULT, timestamp: at(2) }),
      parseToolCall({ result: commitResult("ecd8e01b", "x"), timestamp: at(3) }),
      parseToolCall({ result: commitResult("a19b5ff7", "y"), timestamp: at(4) })
    );
    const s = summarize(tool, { now: Date.parse(at(5)), idleThresholdMs: IDLE_MS });
    expect(formatStageActivityLabel(s)).toBe("remediating · round 2 · lint failing");

    const gate = fromRuntimeState("pr-merge", {
      stage_gate_results: RUNTIME_STATE.stage_gate_results,
    });
    const r = summarize(mergeEvents(tool, gate), {
      now: Date.parse(at(31)),
      idleThresholdMs: IDLE_MS,
    });
    expect(formatStageActivityLabel(r)).toBe("retrying after gate: PR not MERGED");
  });

  it("dedupes the same commit seen twice", () => {
    const one = parseToolCall({ result: commitResult("ecd8e01b", "x"), timestamp: at(3) });
    expect(mergeEvents(one, one)).toHaveLength(1);
  });
});

describe("idle", () => {
  it("a stage with no events turns idle at the threshold (30-minute case)", () => {
    const stageStartedAt = at(0);
    const before = summarize([], {
      now: Date.parse(at(9)),
      stageStartedAt,
      idleThresholdMs: IDLE_MS,
    });
    expect(before.state).toBe("working");
    const after = summarize([], {
      now: Date.parse(at(30)),
      stageStartedAt,
      idleThresholdMs: IDLE_MS,
    });
    expect(after.state).toBe("idle");
    expect(formatStageActivityLabel(after)).toBe("idle 30m");
  });

  it("a recent non-event signal keeps the stage out of idle", () => {
    const s = summarize([], {
      now: Date.parse(at(30)),
      stageStartedAt: at(0),
      lastSignalAt: at(25),
      idleThresholdMs: IDLE_MS,
    });
    expect(s.state).toBe("working");
  });

  it("a zero threshold disables idle", () => {
    const s = summarize([], {
      now: Date.parse(at(300)),
      stageStartedAt: at(0),
      idleThresholdMs: 0,
    });
    expect(s.state).toBe("working");
  });
});
