/**
 * progressMonitor.signalFeed.test.ts
 *
 * Regression + guard tests for Issue #295 — "runaway-progress monitor received
 * zero signals from an active stage."
 *
 * Root cause: skillRunner gated the runaway monitor's signal-classification
 * block on `parsed.toolName` — the SINGULAR field that `parseStreamJsonLine`
 * populates ONLY for `content_block_start` events. The pipeline spawns the
 * Claude CLI with `--output-format stream-json --verbose` and NO
 * `--include-partial-messages`, so at runtime the CLI emits ZERO
 * content_block_start events; every real tool call arrives inside a complete
 * `assistant` message, which the parser exposes as the PLURAL `toolUses[]`
 * array. The feed therefore never fired: `distinct_tool` / `file_change` /
 * `commit` stayed at 0 for the whole stage while cost parsing (independent
 * `result`/`usage` branch) kept working — and a delegation-heavy feature-dev
 * whose productive-signal path also went quiet was false-killed by the
 * no-progress runaway monitor (bowlsheet run #262: "$9.15 spent, activity
 * signals: 0").
 *
 * These tests exercise the two fixes:
 *   1. `recordToolCallProgress` classifies tool calls from BOTH stream shapes,
 *      so feeding the parser a killed-run-shaped `assistant` event yields
 *      totalSignals > 0 (the regression reproduction).
 *   2. `isBlindMonitorKill` — the fail-open guard: parsed tool events > 0 while
 *      signals == 0 means the feed is disconnected, so the kill is suppressed.
 */

import { describe, it, expect } from "vitest";
import { parseStreamJsonLine } from "../../src/utils/tokenParser";
import {
  ProgressMonitor,
  recordToolCallProgress,
  isBlindMonitorKill,
  type ProgressMonitorConfig,
} from "../../src/utils/progressMonitor";

function makeConfig(overrides: Partial<ProgressMonitorConfig> = {}): ProgressMonitorConfig {
  return {
    enabled: true,
    noProgressWindowMs: 120_000,
    minCostToActivateUsd: 0.1,
    catastrophicLimitUsd: 200,
    observeOnly: false,
    churnToolThreshold: 40,
    catastrophicKill: false,
    ...overrides,
  };
}

/**
 * A single-tool `assistant` stream-json line in the EXACT shape the killed run
 * (#262) emitted: tool_use nested in `message.content[]`, `parent_tool_use_id`
 * present as a top-level sibling, per-turn `usage` snapshot on the message.
 */
function assistantToolUseLine(
  toolName: string,
  input: Record<string, unknown>,
  opts: { parentToolUseId?: string | null } = {}
): string {
  return JSON.stringify({
    type: "assistant",
    message: {
      model: "claude-sonnet-5",
      id: "msg_01FIXTURE",
      role: "assistant",
      content: [
        {
          type: "tool_use",
          id: "toolu_01FIXTURE",
          name: toolName,
          input,
          caller: { type: "direct" },
        },
      ],
      usage: { input_tokens: 2, cache_read_input_tokens: 25130, output_tokens: 3 },
    },
    parent_tool_use_id: opts.parentToolUseId ?? null,
    session_id: "sess_fixture",
    uuid: "uuid-fixture",
    timestamp: "2026-07-19T12:00:00.000Z",
    request_id: "req_fixture",
  });
}

describe("Issue #295 — runaway monitor signal feed", () => {
  // ── The regression reproduction ────────────────────────────────────────
  // The disconnect: a real killed-run tool_use event is an `assistant`
  // message. The parser sets the PLURAL toolUses[] and leaves the SINGULAR
  // toolName undefined — so the old `if (parsed?.toolName)` gate skipped it and
  // the monitor recorded nothing.
  it("parser exposes an assistant tool_use as toolUses[], NOT the singular toolName (the disconnect)", () => {
    const parsed = parseStreamJsonLine(assistantToolUseLine("Bash", { command: "git status" }));
    expect(parsed?.type).toBe("assistant");
    // This is why the pre-#295 toolName-gated block never fired at runtime:
    expect(parsed?.toolName).toBeUndefined();
    // The tool call is only reachable via the plural array:
    expect(parsed?.toolUses).toEqual([{ name: "Bash", input: { command: "git status" } }]);
  });

  it("REGRESSION: feeding a killed-run-shaped assistant event yields totalSignals > 0", () => {
    const monitor = new ProgressMonitor(makeConfig());
    // Before feeding: the monitor is blind (this is the #262 kill state).
    expect(monitor.hasObservedAnyProgress).toBe(false);

    const parsed = parseStreamJsonLine(
      assistantToolUseLine("Read", { file_path: "lib/main.dart" })
    );
    const count = recordToolCallProgress(monitor, parsed);

    expect(count).toBe(1);
    // The fix: the feed fired. totalSignals is now > 0 for an active stage.
    expect(monitor.hasObservedAnyProgress).toBe(true);
    expect(monitor.check(10.0).signalsSeen).toBeGreaterThan(0);
  });

  it("reproduces the delegation-heavy feature-dev event mix (43 Bash / 34 Read / 6 Edit / TaskCreate) with signals visibly counted", () => {
    const monitor = new ProgressMonitor(makeConfig());
    // A representative slice of #262's parent-level feature-dev activity, all
    // delivered as `assistant` messages (the runtime shape).
    const lines = [
      assistantToolUseLine("Bash", { command: "flutter test" }),
      assistantToolUseLine("Read", { file_path: "lib/models/game.dart" }),
      assistantToolUseLine("Edit", {
        file_path: "lib/models/frame.dart",
        old_string: "a",
        new_string: "b",
      }),
      assistantToolUseLine("TaskCreate", { subject: "impl scoring", description: "…" }),
      assistantToolUseLine("TaskUpdate", { taskId: "t1", state: "completed" }),
      assistantToolUseLine("Bash", { command: "git commit -m 'feat: scoring'" }),
    ];

    let total = 0;
    for (const line of lines) {
      total += recordToolCallProgress(monitor, parseStreamJsonLine(line));
    }

    expect(total).toBe(6);
    const result = monitor.check(9.15); // the #262 cost at kill
    expect(result.signalsSeen).toBeGreaterThan(0);
    // The Edit (new path) and the git commit are PRODUCTIVE — the window is
    // healthy and the monitor would never kill this stage.
    expect(result.productiveSignals).toBeGreaterThanOrEqual(2);
    // And the fail-open guard is NOT engaged, because signals were recorded.
    expect(isBlindMonitorKill(result.signalsSeen, total)).toBe(false);
  });

  // ── recordToolCallProgress classification ──────────────────────────────
  it("classifies an Edit to a new path as a PRODUCTIVE file_change", () => {
    const monitor = new ProgressMonitor(makeConfig());
    recordToolCallProgress(
      monitor,
      parseStreamJsonLine(assistantToolUseLine("Edit", { file_path: "lib/new.dart" }))
    );
    expect(monitor.check(1.0).productiveSignals).toBe(1);
  });

  it("classifies a `git commit` Bash call as a PRODUCTIVE commit", () => {
    const monitor = new ProgressMonitor(makeConfig());
    recordToolCallProgress(
      monitor,
      parseStreamJsonLine(assistantToolUseLine("Bash", { command: "git commit -m x" }))
    );
    expect(monitor.check(1.0).productiveSignals).toBe(1);
  });

  it("classifies a Read as ACTIVITY only (distinct_tool, never advances the window)", () => {
    const monitor = new ProgressMonitor(makeConfig());
    recordToolCallProgress(
      monitor,
      parseStreamJsonLine(assistantToolUseLine("Read", { file_path: "lib/x.dart" }))
    );
    const result = monitor.check(1.0);
    expect(result.signalsSeen).toBe(1);
    expect(result.productiveSignals).toBe(0);
  });

  it("does NOT treat `git commit --amend` / `--dry-run` as productive (churn cannot fake progress)", () => {
    const monitor = new ProgressMonitor(makeConfig());
    recordToolCallProgress(
      monitor,
      parseStreamJsonLine(assistantToolUseLine("Bash", { command: "git commit --amend --no-edit" }))
    );
    expect(monitor.check(1.0).productiveSignals).toBe(0);
  });

  it("still classifies the singular content_block_start shape (partial-message builds)", () => {
    const monitor = new ProgressMonitor(makeConfig());
    const line = JSON.stringify({
      type: "content_block_start",
      content_block: {
        type: "tool_use",
        id: "toolu_x",
        name: "Write",
        input: { file_path: "a.ts" },
      },
    });
    const parsed = parseStreamJsonLine(line);
    expect(parsed?.toolName).toBe("Write"); // singular field IS set for this shape
    const count = recordToolCallProgress(monitor, parsed);
    expect(count).toBe(1);
    expect(monitor.check(1.0).productiveSignals).toBe(1);
  });

  it("counts every raw tool event even when the distinct signature repeats (dedup does not hide activity)", () => {
    const monitor = new ProgressMonitor(makeConfig());
    // Identical Read three times: distinct_tool dedups (totalSignals=1) but the
    // raw event count is 3 — exactly what the fail-open guard needs.
    let total = 0;
    for (let i = 0; i < 3; i++) {
      total += recordToolCallProgress(
        monitor,
        parseStreamJsonLine(assistantToolUseLine("Read", { file_path: "lib/same.dart" }))
      );
    }
    expect(total).toBe(3);
    expect(monitor.check(1.0).signalsSeen).toBe(1);
  });

  it("handles a null parsed line and non-tool messages without recording (returns 0)", () => {
    const monitor = new ProgressMonitor(makeConfig());
    expect(recordToolCallProgress(monitor, null)).toBe(0);
    expect(
      recordToolCallProgress(
        monitor,
        parseStreamJsonLine('{"type":"result","total_cost_usd":9.15}')
      )
    ).toBe(0);
    expect(monitor.hasObservedAnyProgress).toBe(false);
  });

  // ── Fail-open guard (isBlindMonitorKill) ───────────────────────────────
  describe("isBlindMonitorKill — a blind monitor must never shoot", () => {
    it("SUPPRESSES the kill when tool events were parsed but 0 signals recorded (the #262 state)", () => {
      // This is precisely the disconnect: 100+ tool events parsed, totalSignals=0.
      expect(isBlindMonitorKill(0, 132)).toBe(true);
    });

    it("does NOT suppress when the monitor actually recorded signals (genuine no-progress)", () => {
      // Signals were counted, so the monitor CAN see the stage — a real
      // no-progress kill must still proceed (do not weaken runaway detection).
      expect(isBlindMonitorKill(3, 132)).toBe(false);
    });

    it("does NOT suppress when no tool events were parsed (nothing to vouch for the agent)", () => {
      expect(isBlindMonitorKill(0, 0)).toBe(false);
    });

    it("does NOT suppress when both counts are non-zero", () => {
      expect(isBlindMonitorKill(5, 5)).toBe(false);
    });
  });

  // ── Message-format contract (mirrors skillRunner's emitted marker) ─────
  // HeadlessOrchestrator / retros key on this prefix; lock the format the way
  // skillRunner.costWarn.test.ts locks [cost-warn] / [runaway-ceiling-exceeded].
  it("suppression marker uses the [runaway-progress-feed-disconnect] prefix and names the discrepancy", () => {
    const stage = "feature-dev";
    const parsedToolEventCount = 132;
    const msg =
      `[runaway-progress-feed-disconnect] Stage ${stage}: runaway kill SUPPRESSED (fail-open). ` +
      `Progress monitor recorded 0 signals but ${parsedToolEventCount} tool events were parsed ` +
      `from the stream — the parser→monitor feed is disconnected, the agent is NOT stalled. ` +
      `Refusing to kill a stage the monitor cannot see. (Issue #295)\n`;
    expect(msg).toMatch(/^\[runaway-progress-feed-disconnect\]/);
    expect(msg).toContain("SUPPRESSED (fail-open)");
    expect(msg).toContain("132 tool events were parsed");
    // It must NOT carry a kill marker — this line means the stage LIVES.
    expect(msg).not.toMatch(/runaway-progress-exceeded/);
  });
});
