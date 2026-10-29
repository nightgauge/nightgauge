/**
 * stageActivity - pure per-stage activity model for the pipeline tree (#1897)
 *
 * A running stage used to render as `PR Merge running... [2/14]` whatever it
 * was doing. This module folds the data that already reaches the extension
 * into a summary the tree can render: fix commits, failing checks, failed
 * post-condition gates, recovery attempts, model escalations, and how long
 * the stage has been silent.
 *
 * Two feeds:
 * - Go runtime state (`stageGateResults`, `stageRecoveryAttempts`,
 *   `escalationHistory`) — structured, authoritative.
 * - Tool-call results — HEURISTIC. No orchestrator field names a remediation
 *   round, its failing check or its fix commit; the LLM arm does that work
 *   through Bash, so `parseToolCall` reads git's own `[branch sha] subject`
 *   line and `gh pr checks`-style rows. A missed parse degrades to the old
 *   display, never to a wrong one. A structured event is deferred
 *   (dw-a18632ecef50).
 *
 * No vscode import: unit-tested without the extension host.
 */

export type StageActivityEvent =
  | { kind: "fix-commit"; at: string; sha: string; headline: string }
  | { kind: "check-failing"; at: string; checks: string[] }
  | { kind: "gate-failed"; at: string; gate: string; reason: string }
  | { kind: "recovery"; at: string; action: string; recovered: boolean; reason?: string }
  | { kind: "escalation"; at: string; from: string; to: string; reason: string };

export type StageDisplayState =
  "working" | "remediating" | "retrying-after-gate" | "escalated" | "idle";

export interface StageActivitySummary {
  /** Chronological, deduplicated. */
  events: StageActivityEvent[];
  /** Fix commits pushed since stage start. */
  round: number;
  /** Checks named by the latest check-failing event. */
  failingChecks: string[];
  lastActivityAt?: string;
  state: StageDisplayState;
  /** Milliseconds since the last activity, when known. */
  idleMs?: number;
}

export interface ToolCallLike {
  tool?: string;
  result?: string;
  timestamp: Date | string;
}

/** Runtime-state fields this model reads, as mapped onto PipelineState. */
export interface StageActivityState {
  escalation_history?: Array<{
    stage: string;
    fromModel: string;
    toModel: string;
    reason: string;
    at?: string;
  }>;
  stage_gate_results?: Record<
    string,
    Array<{ gate_name: string; passed: boolean; reason: string; timestamp: string }>
  >;
  stage_recovery_attempts?: Record<
    string,
    Array<{ action: string; recovered: boolean; reason?: string; at: string }>
  >;
}

/** git's commit summary line: `[branch abc1234] subject` (also `[branch (root-commit) sha]`). */
const GIT_COMMIT_LINE = /^\[[^\]\s]+(?: \([^)]*\))? ([0-9a-f]{7,40})\] (.+)$/m;
const FAILING_STATUS = new Set(["fail", "failure", "failed", "failing"]);

function toIso(at: Date | string): string {
  return typeof at === "string" ? at : at.toISOString();
}

/**
 * Events carried by one tool-call result. Heuristic — see the module comment.
 */
export function parseToolCall(event: ToolCallLike): StageActivityEvent[] {
  const result = event.result;
  if (!result) {
    return [];
  }
  const at = toIso(event.timestamp);
  const out: StageActivityEvent[] = [];

  const commit = GIT_COMMIT_LINE.exec(result);
  if (commit) {
    out.push({ kind: "fix-commit", at, sha: commit[1], headline: commit[2].trim() });
  }

  const checks: string[] = [];
  for (const line of result.split("\n")) {
    const cols = line.split(/\t+|\s{2,}/).map((c) => c.trim());
    if (cols.length >= 2 && cols[0] && FAILING_STATUS.has(cols[1].toLowerCase())) {
      checks.push(cols[0]);
    }
  }
  if (checks.length > 0) {
    out.push({ kind: "check-failing", at, checks });
  }
  return out;
}

/** Gate-failed, recovery and escalation events the runtime state holds for a stage. */
export function fromRuntimeState(stage: string, state: StageActivityState): StageActivityEvent[] {
  const out: StageActivityEvent[] = [];
  for (const g of state.stage_gate_results?.[stage] ?? []) {
    if (g.passed === false) {
      out.push({ kind: "gate-failed", at: g.timestamp, gate: g.gate_name, reason: g.reason });
    }
  }
  for (const r of state.stage_recovery_attempts?.[stage] ?? []) {
    out.push({
      kind: "recovery",
      at: r.at,
      action: r.action,
      recovered: r.recovered,
      ...(r.reason ? { reason: r.reason } : {}),
    });
  }
  for (const e of state.escalation_history ?? []) {
    if (e.stage === stage) {
      out.push({
        kind: "escalation",
        at: e.at ?? "",
        from: e.fromModel,
        to: e.toModel,
        reason: e.reason,
      });
    }
  }
  return out;
}

function eventKey(e: StageActivityEvent): string {
  switch (e.kind) {
    case "fix-commit":
      return `fix-commit:${e.sha}`;
    case "check-failing":
      return `check-failing:${e.at}:${e.checks.join(",")}`;
    case "gate-failed":
      return `gate-failed:${e.at}:${e.gate}`;
    case "recovery":
      return `recovery:${e.at}:${e.action}`;
    case "escalation":
      return `escalation:${e.at}:${e.from}:${e.to}`;
  }
}

/** Merge event lists, dropping duplicates and sorting chronologically (stable). */
export function mergeEvents(...lists: StageActivityEvent[][]): StageActivityEvent[] {
  const seen = new Set<string>();
  const merged: StageActivityEvent[] = [];
  for (const list of lists) {
    for (const e of list) {
      const key = eventKey(e);
      if (!seen.has(key)) {
        seen.add(key);
        merged.push(e);
      }
    }
  }
  return merged
    .map((e, i) => ({ e, i, t: Date.parse(e.at) }))
    .sort((a, b) => {
      const ta = Number.isNaN(a.t) ? Infinity : a.t;
      const tb = Number.isNaN(b.t) ? Infinity : b.t;
      return ta === tb ? a.i - b.i : ta - tb;
    })
    .map((x) => x.e);
}

export interface SummarizeOptions {
  now: number;
  stageStartedAt?: string;
  /** Latest non-event signal: phase, token or tool-call activity. */
  lastSignalAt?: string;
  idleThresholdMs: number;
}

function maxIso(values: Array<string | undefined>): string | undefined {
  let best: string | undefined;
  let bestT = -Infinity;
  for (const v of values) {
    if (!v) continue;
    const t = Date.parse(v);
    if (!Number.isNaN(t) && t > bestT) {
      bestT = t;
      best = v;
    }
  }
  return best;
}

/** Decide the display state. First match wins: idle, escalated, retrying, remediating, working. */
export function summarize(
  events: StageActivityEvent[],
  opts: SummarizeOptions
): StageActivitySummary {
  const round = events.filter((e) => e.kind === "fix-commit").length;
  const lastFailing = [...events].reverse().find((e) => e.kind === "check-failing");
  const failingChecks = lastFailing?.kind === "check-failing" ? lastFailing.checks : [];
  const lastActivityAt = maxIso([
    ...events.map((e) => e.at),
    opts.lastSignalAt,
    opts.stageStartedAt,
  ]);
  const idleMs =
    lastActivityAt !== undefined ? Math.max(0, opts.now - Date.parse(lastActivityAt)) : undefined;
  const latest = events[events.length - 1];

  let state: StageDisplayState;
  if (idleMs !== undefined && opts.idleThresholdMs > 0 && idleMs >= opts.idleThresholdMs) {
    state = "idle";
  } else if (latest?.kind === "escalation") {
    state = "escalated";
  } else if (latest?.kind === "gate-failed" || latest?.kind === "recovery") {
    state = "retrying-after-gate";
  } else if (round > 0 || failingChecks.length > 0) {
    state = "remediating";
  } else {
    state = "working";
  }
  return { events, round, failingChecks, lastActivityAt, state, idleMs };
}

/** Compact duration: `45s`, `12m`, `1h 5m`. */
export function formatDuration(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000));
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  return `${h}h ${m % 60}m`;
}

/**
 * The state label shown in a running stage's description. Returns "" for
 * `working`, where the caller keeps its existing phase label.
 */
export function formatStageActivityLabel(summary: StageActivitySummary): string {
  const latest = summary.events[summary.events.length - 1];
  switch (summary.state) {
    case "idle":
      return `idle ${formatDuration(summary.idleMs ?? 0)}`;
    case "escalated":
      return latest?.kind === "escalation"
        ? `escalated ${latest.from} → ${latest.to}`
        : "escalated";
    case "retrying-after-gate": {
      const gate = [...summary.events].reverse().find((e) => e.kind === "gate-failed");
      return gate?.kind === "gate-failed"
        ? `retrying after gate: ${gate.reason}`
        : "retrying after gate";
    }
    case "remediating": {
      const parts = ["remediating"];
      if (summary.round > 0) parts.push(`round ${summary.round}`);
      if (summary.failingChecks.length > 0) {
        parts.push(`${summary.failingChecks.join(", ")} failing`);
      }
      return parts.join(" · ");
    }
    case "working":
      return "";
  }
}
