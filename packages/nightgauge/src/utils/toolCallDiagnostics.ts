/**
 * Observability for long-running tool calls and the tasks they poll (#1118).
 *
 * ## What this module does NOT do
 *
 * It never withholds activity, never kills, and never makes a stage killable
 * that was not killable before. Every verdict here is REPORTING. That is a
 * deliberate correction, and the reason is worth stating in full because the
 * opposite design is extremely tempting.
 *
 * `WEDGED_TOOL_CALL_CEILING_S` in skillRunner (#1083) is compared against ONE
 * tool call's `elapsedSeconds`, and a task polled by a series of short bounded
 * calls never reaches it: 15 calls, none above ~4.5 minutes, because the
 * polling tool is invoked with a 5-minute block timeout, so every call returns
 * cleanly and `elapsedSeconds` resets to zero. It is very natural to conclude
 * that the clock should be keyed to the polled TASK instead, and that once a
 * task has produced no new output for the same budget its heartbeats should
 * stop counting as activity, so the existing machinery can reclaim the stage.
 *
 * That conclusion is wrong, and the run that motivated this issue is itself
 * the counter-example. The codegen step that looked wedged — 15 polls, no
 * incremental output, zero CPU — **completed successfully after ~24 minutes**.
 * Its output was piped through `tail`, so it buffered: it produced no
 * incremental output at all, by construction. Under a task-keyed kill
 * predicate it would have been declared wedged at minute 20 and the stage
 * reclaimed four minutes before it succeeded, destroying the worktree with it.
 *
 * The general point: **"produced no new output" does not distinguish a wedged
 * task from a slow one.** Any command that buffers behind a pipe, or that is
 * simply quiet during a long compile, is indistinguishable from a deadlock
 * from the outside. There is no safe purely-observational signal here, and a
 * false positive costs a killed build plus a destroyed worktree — strictly
 * worse than the bug. So this module measures the silence and reports it in
 * terms the operator can judge, and the decision stays with them.
 *
 * ## What it does do
 *
 *  - Tracks how long each polled task has gone without producing new output,
 *    across however many calls observe it, and says so once the silence gets
 *    long. The message states the observation, never a verdict of "wedged".
 *  - Makes a long call legible before the ceiling. Heartbeats were consumed
 *    silently, so "is this hung?" could only be answered with `ps` and `lsof`.
 *  - Surfaces a call that has outlived the timeout it declared for itself.
 *    One observed run sat at 17 minutes against a declared 300000 ms — a
 *    threefold overrun with no mention of it anywhere.
 *  - Reports the working tree by change-kind when the per-call ceiling fires,
 *    so the operator learns what is at stake without running git by hand.
 *
 * Nothing here spawns a process. The one git read it needs is injected.
 */

/**
 * How often an in-flight tool call is reported while it is still under the
 * wedge ceiling.
 *
 * Not every heartbeat: the adapter emits one roughly every 30 seconds, so a
 * per-heartbeat line would be ~40 lines before the 20-minute ceiling — noise
 * that trains operators to skip it, and that buries the ceiling line itself.
 * Not once either: a single line at the start tells nobody whether the call
 * is still where it was.
 *
 * Five minutes is the coarsest cadence that still answers the question in
 * time. Against the 20-minute ceiling it yields at most three lines (5, 10,
 * 15) before the wedge line, each carrying elapsed time and the distance left
 * to the ceiling — enough to watch a call approach the boundary and decide,
 * while it is still running, whether to look closer.
 */
export const LONG_TOOL_CALL_REPORT_INTERVAL_S = 5 * 60;

/**
 * Which reporting window an elapsed time falls into. Bucket 0 is the first
 * interval and is deliberately never reported: a call under five minutes is
 * unremarkable.
 */
export function longToolCallReportBucket(elapsedSeconds: number): number {
  if (!Number.isFinite(elapsedSeconds) || elapsedSeconds <= 0) return 0;
  return Math.floor(elapsedSeconds / LONG_TOOL_CALL_REPORT_INTERVAL_S);
}

/**
 * The timeout a tool call declared for itself, in milliseconds.
 *
 * Bash and TaskOutput both carry `timeout` in ms. Anything non-numeric,
 * non-finite or non-positive is "not declared" — a fabricated limit would be
 * worse than none.
 */
export function declaredTimeoutMs(input: unknown): number | undefined {
  if (typeof input !== "object" || input === null) return undefined;
  const record = input as Record<string, unknown>;
  for (const key of ["timeout", "timeout_ms", "timeoutMs"]) {
    const value = record[key];
    if (typeof value === "number" && Number.isFinite(value) && value > 0) return value;
  }
  return undefined;
}

function describeDeclaredTimeout(elapsedSeconds: number, timeoutMs: number | undefined): string {
  if (timeoutMs === undefined) return "";
  const timeoutSeconds = timeoutMs / 1000;
  if (timeoutSeconds <= 0) return "";
  const factor = elapsedSeconds / timeoutSeconds;
  return (
    ` It declared a ${timeoutMs}ms timeout` +
    (elapsedSeconds > timeoutSeconds ? ` and is ${factor.toFixed(1)}× past it.` : ".")
  );
}

/**
 * The periodic "still running" line for a call that has not reached the
 * ceiling. Names the tool, its elapsed time, and where that sits against the
 * ceiling, so a wedge is distinguishable from a slow call without `ps`.
 */
export function formatLongToolCall(args: {
  stage: string;
  toolName?: string;
  elapsedSeconds: number;
  ceilingSeconds: number;
  declaredTimeoutMs?: number;
}): string {
  const { stage, toolName, elapsedSeconds, ceilingSeconds } = args;
  const elapsed = Math.round(elapsedSeconds);
  const remaining = Math.max(0, Math.round(ceilingSeconds - elapsedSeconds));
  const percent = ceilingSeconds > 0 ? Math.round((elapsedSeconds / ceilingSeconds) * 100) : 0;
  return (
    `[long-tool-call] Stage ${stage}: ${toolName ?? "a tool call"} has been running for ` +
    `${elapsed}s — ${percent}% of the ${ceilingSeconds}s wedge ceiling, ${remaining}s remaining.` +
    `${describeDeclaredTimeout(elapsedSeconds, args.declaredTimeoutMs)} (#1118)\n`
  );
}

/**
 * The one-shot line for a call that has outlived the timeout it declared.
 * Independent of the wedge ceiling: a 300000ms call at 900s is already wrong
 * long before 1200s.
 */
export function formatTimeoutOverrun(args: {
  stage: string;
  toolName?: string;
  elapsedSeconds: number;
  declaredTimeoutMs: number;
}): string {
  const { stage, toolName, elapsedSeconds, declaredTimeoutMs: timeoutMs } = args;
  const elapsed = Math.round(elapsedSeconds);
  const factor = (elapsedSeconds / (timeoutMs / 1000)).toFixed(1);
  return (
    `[tool-call-timeout-exceeded] Stage ${stage}: ${toolName ?? "a tool call"} declared a ` +
    `${timeoutMs}ms timeout but has been running for ${elapsed}s — ${factor}× its own limit. ` +
    `The declared timeout is not enforced here; this is the only notice of the overrun. ` +
    `(#1118)\n`
  );
}

/** Counts of working-tree entries by change kind. */
export interface WorktreeChangeSummary {
  modified: number;
  added: number;
  deleted: number;
  renamed: number;
  untracked: number;
  total: number;
}

/**
 * Classify one `git status --porcelain` line.
 *
 * Both columns are consulted: a codegen step that deletes tracked outputs
 * leaves ` D` (unstaged) or `D ` (staged) depending on what touched the index,
 * and a summary that saw only one column would report zero deletions for the
 * exact tree this issue is about.
 */
function classifyPorcelainLine(line: string): keyof Omit<WorktreeChangeSummary, "total"> | null {
  if (line.length < 3) return null;
  const x = line[0];
  const y = line[1];
  if (x === "?" || y === "?") return "untracked";
  if (x === "R" || y === "R") return "renamed";
  if (x === "D" || y === "D") return "deleted";
  if (x === "A" || y === "A") return "added";
  if (x === "M" || y === "M" || x === "T" || y === "T") return "modified";
  return null;
}

/** Parse the whole of `git status --porcelain` into counts. */
export function parseWorktreeStatus(porcelain: string): WorktreeChangeSummary {
  const summary: WorktreeChangeSummary = {
    modified: 0,
    added: 0,
    deleted: 0,
    renamed: 0,
    untracked: 0,
    total: 0,
  };
  for (const line of porcelain.split("\n")) {
    if (line.trim().length === 0) continue;
    const kind = classifyPorcelainLine(line);
    if (!kind) continue;
    summary[kind] += 1;
    summary.total += 1;
  }
  return summary;
}

/**
 * Read-only snapshot of the working tree at `worktreePath`.
 *
 * The git call is injected rather than made here: this module stays free of
 * process spawning, and the one caller that needs it owns the exemption for a
 * short-timeout synchronous read.
 *
 * Returns `undefined` and never throws when git is unavailable, the path is
 * not a repository, or the call times out. This runs on a stage that is
 * already in trouble; a diagnostic that can throw into the stage it is
 * describing is worse than no diagnostic.
 */
export function summarizeWorktreeChanges(
  worktreePath: string,
  readPorcelainStatus: (path: string) => string
): WorktreeChangeSummary | undefined {
  try {
    return parseWorktreeStatus(readPorcelainStatus(worktreePath));
  } catch {
    return undefined;
  }
}

/**
 * One-line rendering of the tree the wedge is leaving behind. Empty string
 * when git could not be read, so the caller can concatenate unconditionally.
 */
export function formatWorktreeSummary(
  worktreePath: string,
  summary: WorktreeChangeSummary | undefined
): string {
  if (!summary) return "";
  if (summary.total === 0) return ` Worktree ${worktreePath} is clean.`;
  const parts: string[] = [];
  if (summary.modified > 0) parts.push(`${summary.modified} modified`);
  if (summary.added > 0) parts.push(`${summary.added} added`);
  if (summary.deleted > 0) parts.push(`${summary.deleted} deleted`);
  if (summary.renamed > 0) parts.push(`${summary.renamed} renamed`);
  if (summary.untracked > 0) parts.push(`${summary.untracked} untracked`);
  return ` Worktree ${worktreePath} holds ${summary.total} changed entries: ${parts.join(", ")}.`;
}

// ───────────────────── polled-task silence (report only) ─────────────────────

/**
 * Tools that OBSERVE an underlying task rather than perform work themselves.
 *
 * Each returns whatever the task has produced since the last look and then
 * exits, which is why a series of them can outlive any single-call ceiling
 * while the thing they are watching says nothing at all.
 */
const POLLING_TOOL_NAMES = new Set(["TaskOutput", "BashOutput", "AgentOutput"]);

/** Input keys under which a polling tool names the task it is observing. */
const POLLED_TASK_ID_KEYS = ["task_id", "taskId", "bash_id", "bashId", "shell_id", "agent_id"];

/**
 * The stable identity of the task a call is polling, or `undefined` when the
 * call is not a poll.
 *
 * Namespaced by tool so a `bash_id` and a `task_id` that happen to collide are
 * never merged into one clock.
 */
export function identifyPolledTask(name: string, input: unknown): string | undefined {
  if (!POLLING_TOOL_NAMES.has(name)) return undefined;
  if (typeof input !== "object" || input === null) return undefined;
  const record = input as Record<string, unknown>;
  for (const key of POLLED_TASK_ID_KEYS) {
    const value = record[key];
    if (typeof value === "string" && value.length > 0) return `${name}:${value}`;
    if (typeof value === "number" && Number.isFinite(value)) return `${name}:${value}`;
  }
  return undefined;
}

/**
 * FNV-1a over the whole result body. A truncated comparison would call two
 * outputs identical because they share a prefix — precisely what a poll that
 * re-echoes a long unchanged buffer looks like.
 */
function signature(text: string): string {
  let hash = 0x811c9dc5;
  for (let i = 0; i < text.length; i++) {
    hash ^= text.charCodeAt(i);
    hash = Math.imul(hash, 0x01000193);
  }
  return `${text.length}:${(hash >>> 0).toString(16)}`;
}

interface PolledTaskState {
  /** Distinct poll calls seen against this task. */
  polls: number;
  /** Last time the task produced output different from the look before it. */
  lastOutputAtMs: number;
  /** Signature of the most recent result, or undefined before the first. */
  lastSignature?: string;
  /** Consecutive looks that returned exactly what the look before them did. */
  unchangedLooks: number;
  /** True once this silence has been reported; cleared by real output. */
  silenceReported: boolean;
}

/** An observation about a quiet task. Deliberately not a verdict. */
export interface PolledTaskSilence {
  taskId: string;
  polls: number;
  silentForSeconds: number;
  unchangedLooks: number;
}

/**
 * Tracks every task a stage is polling and how long each has gone without
 * producing new output.
 *
 * The tracker answers "how quiet has this been, and for how long" — never "is
 * this wedged". See the module header for why no purely-observational signal
 * can answer the second question, and what it cost to assume otherwise.
 *
 * Bounded: a stage can poll many tasks over its life and this must not grow
 * with them.
 */
export class PolledTaskSilenceTracker {
  private static readonly MAX_TASKS = 128;
  private static readonly MAX_CALL_BINDINGS = 256;

  private readonly tasks = new Map<string, PolledTaskState>();
  /** tool_use id to task key, so a heartbeat carrying only an id resolves. */
  private readonly callToTask = new Map<string, string>();

  /**
   * @param budgetMs how long a task may say nothing before it is worth
   *   mentioning. A reporting threshold, never a kill threshold.
   */
  constructor(private readonly budgetMs: number) {}

  /**
   * Register a poll. Returns the task key when the call is a poll, so the
   * caller can bind heartbeats to it.
   */
  observePoll(
    name: string,
    input: unknown,
    toolUseId: string | undefined,
    nowMs: number
  ): string | undefined {
    const taskId = identifyPolledTask(name, input);
    if (!taskId) return undefined;

    let state = this.tasks.get(taskId);
    if (!state) {
      if (this.tasks.size >= PolledTaskSilenceTracker.MAX_TASKS) {
        const oldest = this.tasks.keys().next();
        if (!oldest.done) this.tasks.delete(oldest.value);
      }
      // A task with no result yet has said nothing since it was first looked
      // at; that is the clock's origin.
      state = { polls: 0, lastOutputAtMs: nowMs, unchangedLooks: 0, silenceReported: false };
      this.tasks.set(taskId, state);
    }
    state.polls += 1;

    if (toolUseId !== undefined) {
      if (this.callToTask.size >= PolledTaskSilenceTracker.MAX_CALL_BINDINGS) {
        const oldest = this.callToTask.keys().next();
        if (!oldest.done) this.callToTask.delete(oldest.value);
      }
      this.callToTask.set(toolUseId, taskId);
    }
    return taskId;
  }

  /**
   * Bind a poll's result to its task. Output that differs from the previous
   * look is the only thing that restarts the clock — but a look that repeats
   * the previous one proves only that the task is quiet, not that it is stuck.
   */
  observeResult(toolUseId: string, resultText: string | undefined, nowMs: number): void {
    const taskId = this.callToTask.get(toolUseId);
    if (!taskId) return;
    const state = this.tasks.get(taskId);
    if (!state) return;

    const sig = signature(resultText ?? "");
    if (state.lastSignature === sig) {
      state.unchangedLooks += 1;
      return;
    }
    state.lastSignature = sig;
    state.unchangedLooks = 0;
    state.lastOutputAtMs = nowMs;
    // The task spoke; a later silence must be reportable afresh.
    state.silenceReported = false;
  }

  /** The task a heartbeat's tool call is polling, if any. */
  taskForToolUse(toolUseId: string): string | undefined {
    return this.callToTask.get(toolUseId);
  }

  /**
   * True when this task has been polled repeatedly and has produced nothing
   * new for longer than the budget.
   *
   * The `polls >= 2` requirement keeps this about the cross-call case: one
   * call sitting on one task is what the per-call ceiling already reports.
   */
  hasBeenSilent(taskId: string, nowMs: number): boolean {
    const state = this.tasks.get(taskId);
    if (!state) return false;
    return state.polls >= 2 && nowMs - state.lastOutputAtMs >= this.budgetMs;
  }

  /**
   * The observation to report, or `undefined` when there is nothing new to
   * say — either the task has not been quiet long enough, or this silence has
   * already been reported. One line per silence, not one per 30-second
   * heartbeat.
   */
  silenceToReport(taskId: string, nowMs: number): PolledTaskSilence | undefined {
    const state = this.tasks.get(taskId);
    if (!state || state.silenceReported) return undefined;
    if (!this.hasBeenSilent(taskId, nowMs)) return undefined;
    state.silenceReported = true;
    return {
      taskId,
      polls: state.polls,
      silentForSeconds: Math.round((nowMs - state.lastOutputAtMs) / 1000),
      unchangedLooks: state.unchangedLooks,
    };
  }
}

/**
 * The line for a task that has been quiet across a series of polls.
 *
 * It states the observation and hands the judgement to the operator. It must
 * never read as a verdict: the run this issue came from looked exactly like
 * this and finished successfully at ~24 minutes, because its output was
 * buffered behind a pipe.
 */
export function formatLongSilentTask(args: {
  stage: string;
  toolName?: string;
  silence: PolledTaskSilence;
  worktreeSummary: string;
}): string {
  const { stage, toolName, silence, worktreeSummary } = args;
  const minutes = Math.round(silence.silentForSeconds / 60);
  return (
    `[long-silent-task] Stage ${stage}: ${toolName ?? "a tool call"} has polled ` +
    `${silence.taskId} ${silence.polls} times over ${minutes}m (${silence.silentForSeconds}s) ` +
    `with no new output. It may be wedged or simply quiet — a command that buffers behind a ` +
    `pipe, or a long compile, looks identical to a deadlock from here. Check CPU and recent ` +
    `writes before killing it. Nothing has been killed or deferred on account of this.` +
    `${worktreeSummary} (#1118)\n`
  );
}
