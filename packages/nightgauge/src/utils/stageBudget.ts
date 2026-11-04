/**
 * Non-USD stage budgets for editor-launched stages (#1668).
 *
 * The Go executor stops a stage at its turn, wall-clock and token budgets
 * (#1652, `internal/execution/stage_budget.go`). A stage the extension
 * launches itself through `runStageSkillHeadless` never passes through that
 * executor, so this module is its twin on the extension side. It does NOT
 * resolve the budgets: the ceilings, the ADR-023 Q8 defaults and the
 * zero-cost floor come from the Go binary over `pipeline.resolveStageBudgets`,
 * so they live in one place.
 *
 * What it does own:
 *   - counting the stage's turns and tokens from its stdout as it streams,
 *     and its wall clock from spawn, and reporting the first breach;
 *   - the per-step peak prompt size, the numerator of the stage's
 *     context-window utilization (#1653), as the max over steps, never a sum;
 *   - terminating the stage's whole process tree on a breach and verifying
 *     no member survives.
 *
 * A breach is reported as `stage_budget_exceeded:<dimension> observed=<n>
 * ceiling=<n>`, the reason #1652 stamps, so the run's failed transition is
 * classified `budget_exceeded` by the existing budget-enforcer terminal rule
 * and is not retried.
 *
 * @see docs/decisions/023-model-aware-context-budgets.md
 * @see internal/execution/stage_budget.go
 */

import { execFile } from "child_process";
import { listProcessTree } from "./processTree";

/** The reason a stage stopped at a stage budget stamps, before ":<dimension>". */
export const STAGE_BUDGET_EXCEEDED = "stage_budget_exceeded";

/** The three stage budget dimensions, as the Go executor names them. */
export type StageBudgetDimension = "turns" | "wall_clock" | "tokens";

/** The resolved budget of one dispatch, as `pipeline.resolveStageBudgets` returns it. */
export interface StageBudgets {
  /** A positive ceiling binds; -1 is unlimited (priced stages only). */
  maxTurns: number;
  maxWallClockMs: number;
  maxTokens: number;
  /** No USD cap binds the stage: a local or $0-priced model. */
  zeroCost: boolean;
  /** The context window the dispatch runs with; absent or 0 when unknown. */
  contextWindowTokens?: number;
  /** Lines the executor would log for this dispatch, such as a refused -1. */
  warnings?: string[];
}

/** What a dispatch asks the resolver about. */
export interface StageBudgetRequest {
  repo: string;
  stage: string;
  adapter: string;
  model: string;
}

/** Resolves a dispatch's budgets; in the extension, the IPC call to Go. */
export type StageBudgetResolver = (request: StageBudgetRequest) => Promise<StageBudgets>;

let registeredResolver: StageBudgetResolver | null = null;

/**
 * Register the resolver `runStageSkillHeadless` asks (bootstrap wires it to
 * `pipeline.resolveStageBudgets`). `null` unregisters it: with no resolver
 * the extension has no Go binary to ask, and stages run as before.
 */
export function setStageBudgetResolver(resolver: StageBudgetResolver | null): void {
  registeredResolver = resolver;
}

/** The registered resolver, or null. */
export function getStageBudgetResolver(): StageBudgetResolver | null {
  return registeredResolver;
}

/**
 * Resolve a dispatch's budgets through the registered resolver. Rejects when
 * no resolver is registered or the resolver fails.
 */
export async function resolveStageBudgets(request: StageBudgetRequest): Promise<StageBudgets> {
  const resolver = registeredResolver;
  if (!resolver) throw new Error("no stage budget resolver is registered");
  return resolver(request);
}

/** One stage's first budget breach. */
export interface StageBudgetBreach {
  dimension: StageBudgetDimension;
  observed: number;
  ceiling: number;
}

/**
 * The failed transition's error for a breach:
 * `stage_budget_exceeded:<dimension> observed=<n> ceiling=<n>`.
 */
export function stageBudgetErrorMessage(breach: StageBudgetBreach): string {
  return (
    `${STAGE_BUDGET_EXCEEDED}:${breach.dimension} observed=${breach.observed} ` +
    `ceiling=${breach.ceiling}: the stage reached its ${noun(breach.dimension)} budget and ` +
    `was stopped (pipeline.stage_budgets, docs/GUARDRAILS_AND_BUDGETS.md)`
  );
}

/** The error of a zero-cost stage refused because its budgets did not resolve. */
export function stageBudgetUnresolvedMessage(stage: string, model: string, reason: string): string {
  return (
    `[stage-budget] refused: stage ${stage} on zero-cost model ${model || "(unknown)"} has no ` +
    `binding budget because pipeline.resolveStageBudgets failed (${reason}); no USD cap can ` +
    `stop a $0 model, so the stage is not run (#1668)`
  );
}

function noun(dimension: StageBudgetDimension): string {
  return dimension === "wall_clock" ? "wall-clock" : dimension === "tokens" ? "token" : "turn";
}

/** The stream a stage's stdout carries, which decides what a turn is. */
export type StageBudgetStreamFormat = "claude" | "sdk";

/** Per-step token counts, as OpenCode reports them in `step_finish.part.tokens`. */
export interface StepTokens {
  input: number;
  output: number;
  reasoning: number;
  cacheRead: number;
  cacheWrite: number;
}

export interface StageBudgetEnforcerOptions {
  format: StageBudgetStreamFormat;
  /** Called once, on the first breach. */
  onBreach: (breach: StageBudgetBreach) => void;
  /** The clock; a test may replace it. */
  now?: () => number;
  /**
   * The time inside [sinceMs, nowMs] the wall clock leaves out: pr-merge's
   * recorded merge-queue wait (#2770), which the binary reports. Asked only
   * when the wall-clock timer fires; the timer is then re-armed for what is
   * left. A rejected or invalid answer leaves nothing out.
   */
  excludedMs?: (sinceMs: number, nowMs: number) => Promise<number>;
}

function nonNegative(n: unknown): number {
  return typeof n === "number" && Number.isFinite(n) && n > 0 ? n : 0;
}

/**
 * One stage's budget and what its stream has used of it. Counters run from
 * spawn whether or not the budget has arrived, so a budget resolved after
 * the process started still judges everything the stream has shown.
 */
export class StageBudgetEnforcer {
  private limits: StageBudgets | undefined;
  private turns = 0;
  /** Whether the latest turn asked for another (a tool call, or no "stop"). */
  private lastAsksAnother = false;
  private readonly seenMessages = new Set<string>();
  private streamTokens = 0;
  private accumulatedTokens = 0;
  private peak = 0;
  private startedAt: number | undefined;
  private timer: ReturnType<typeof setTimeout> | undefined;
  /** True while a fired wall clock is asking for its excluded time. */
  private checkingWallClock = false;
  private disarmed = false;
  private breachValue: StageBudgetBreach | undefined;
  private readonly now: () => number;

  constructor(private readonly options: StageBudgetEnforcerOptions) {
    this.now = options.now ?? Date.now;
  }

  /** Start the wall clock: the stage's process has been spawned. */
  start(): void {
    this.startedAt = this.now();
    this.armWallClock();
  }

  /** Apply the resolved budget, and judge what the stream has shown so far. */
  arm(limits: StageBudgets): void {
    if (this.breachValue) return;
    this.limits = limits;
    this.armWallClock();
    this.checkTokens();
    this.checkTurns(false);
  }

  /** Whether a budget is in force. */
  get armed(): boolean {
    return this.limits !== undefined;
  }

  /** Stop the wall clock: the stage has ended. */
  disarm(): void {
    this.disarmed = true;
    if (this.timer !== undefined) clearTimeout(this.timer);
    this.timer = undefined;
  }

  /** The first breach, or undefined. */
  get breach(): StageBudgetBreach | undefined {
    return this.breachValue;
  }

  /** The largest prompt one model step sent, 0 when no step reported one. */
  get peakStepInputTokens(): number {
    return this.peak;
  }

  /** The window the stage ran with, 0 when unknown. */
  get contextWindowTokens(): number {
    return nonNegative(this.limits?.contextWindowTokens);
  }

  /** The turns the stream has shown so far. */
  get turnCount(): number {
    return this.turns;
  }

  /** The tokens counted against the token budget so far. */
  get tokensUsed(): number {
    return Math.max(this.streamTokens, this.accumulatedTokens);
  }

  /**
   * The stage's running token total from its usage accumulator: input,
   * output and cache writes. Cache reads do not count (#1652).
   */
  observeTokenTotal(total: number): void {
    this.accumulatedTokens = Math.max(this.accumulatedTokens, nonNegative(total));
    this.checkTokens();
  }

  /** One complete stdout line of the stage. */
  observeLine(line: string): void {
    if (this.breachValue) return;
    const trimmed = line.trim();
    if (!trimmed.startsWith("{")) return;
    if (this.options.format === "sdk") {
      if (!trimmed.includes('"adapter activity"')) return;
      this.observeActivity(trimmed);
      return;
    }
    if (!trimmed.includes('"assistant"')) return;
    this.observeClaudeAssistant(trimmed);
  }

  /**
   * An SDK stage CLI activity line (#1657): for OpenCode, one per stream
   * event, with `step_finish` carrying the step's reason and tokens.
   */
  private observeActivity(line: string): void {
    let data:
      | {
          event?: unknown;
          reason?: unknown;
          tokens?: Partial<StepTokens>;
          turn?: unknown;
          asksAnother?: unknown;
        }
      | undefined;
    try {
      data = (JSON.parse(line) as { data?: typeof data }).data;
    } catch {
      return;
    }
    if (!data) return;
    if (data.event === "step_finish") {
      this.turns++;
      this.lastAsksAnother = data.reason !== "stop";
      if (data.tokens) this.observeStep(data.tokens);
      this.checkTurns(false);
    } else if (data.event === "step_start") {
      // A step begun past the budget is a turn begun.
      this.checkTurns(true);
    } else if (data.turn === true) {
      // codex, gemini and grok: the SDK stage CLI marks the turns the Go
      // executor counts (forwardCliTurnActivity).
      this.turns++;
      this.lastAsksAnother = data.asksAnother === true;
      this.checkTurns(false);
    } else if (data.asksAnother === true) {
      // grok: a tool call asks for another turn.
      this.lastAsksAnother = true;
      this.checkTurns(false);
    }
  }

  /** OpenCode's per-step usage: its tokens count, and its prompt is a peak candidate. */
  private observeStep(tokens: Partial<StepTokens>): void {
    const input = nonNegative(tokens.input);
    const cacheRead = nonNegative(tokens.cacheRead);
    const cacheWrite = nonNegative(tokens.cacheWrite);
    this.peak = Math.max(this.peak, input + cacheRead + cacheWrite);
    this.streamTokens +=
      input + nonNegative(tokens.output) + nonNegative(tokens.reasoning) + cacheWrite;
    this.checkTokens();
  }

  /**
   * A claude stream assistant envelope: a main-thread message is one turn,
   * counted once by its id however many content blocks carry it, and it
   * asks for another when a block is a tool_use. A subagent's messages are
   * not the stage's turns, as for the CLI's own --max-turns.
   */
  private observeClaudeAssistant(line: string): void {
    let event:
      | {
          type?: unknown;
          parent_tool_use_id?: unknown;
          message?: { id?: unknown; content?: Array<{ type?: unknown }> };
        }
      | undefined;
    try {
      event = JSON.parse(line) as typeof event;
    } catch {
      return;
    }
    if (!event || event.type !== "assistant" || !event.message) return;
    if (typeof event.parent_tool_use_id === "string" && event.parent_tool_use_id !== "") return;
    const id = typeof event.message.id === "string" ? event.message.id : "";
    // A message without an id cannot be told apart from the next, so each
    // counts as its own turn: over-counting stops a stage early,
    // under-counting would never stop it.
    if (id === "" || !this.seenMessages.has(id)) {
      if (id !== "") this.seenMessages.add(id);
      this.turns++;
      this.lastAsksAnother = false;
    }
    if ((event.message.content ?? []).some((block) => block?.type === "tool_use")) {
      this.lastAsksAnother = true;
    }
    this.checkTurns(false);
  }

  private armWallClock(excludedMs = 0): void {
    if (this.timer !== undefined || this.checkingWallClock || this.disarmed) return;
    if (this.startedAt === undefined || !this.limits) return;
    const limit = this.limits.maxWallClockMs;
    if (!(limit > 0)) return;
    const remaining = Math.max(0, limit - (this.now() - this.startedAt - excludedMs));
    this.timer = setTimeout(() => {
      this.timer = undefined;
      void this.wallClockFired(limit);
    }, remaining);
    if (typeof this.timer.unref === "function") this.timer.unref();
  }

  /**
   * The wall clock's timer fired. The counted time is elapsed time less what
   * `excludedMs` leaves out (never more than elapsed); under the limit the
   * timer is re-armed for the rest, else the breach records the counted time.
   */
  private async wallClockFired(limit: number): Promise<void> {
    const startedAt = this.startedAt ?? this.now();
    let excluded = 0;
    if (this.options.excludedMs) {
      this.checkingWallClock = true;
      try {
        excluded = nonNegative(await this.options.excludedMs(startedAt, this.now()));
      } catch {
        excluded = 0;
      } finally {
        this.checkingWallClock = false;
      }
    }
    if (this.disarmed || this.breachValue) return;
    const elapsed = this.now() - startedAt;
    const counted = elapsed - Math.min(excluded, elapsed);
    if (counted < limit) {
      this.armWallClock(elapsed - counted);
      return;
    }
    this.record({ dimension: "wall_clock", observed: counted, ceiling: limit });
  }

  private checkTokens(): void {
    const limit = this.limits?.maxTokens ?? 0;
    const used = this.tokensUsed;
    if (limit > 0 && used > limit) {
      this.record({ dimension: "tokens", observed: used, ceiling: limit });
    }
  }

  private checkTurns(stepBegun: boolean): void {
    const limit = this.limits?.maxTurns ?? 0;
    if (!(limit > 0)) return;
    if (this.turns > limit || (this.turns === limit && (this.lastAsksAnother || stepBegun))) {
      this.record({
        dimension: "turns",
        observed: stepBegun ? this.turns + 1 : this.turns,
        ceiling: limit,
      });
    }
  }

  private record(breach: StageBudgetBreach): void {
    if (this.breachValue) return;
    this.breachValue = breach;
    this.disarm();
    this.options.onBreach(breach);
  }
}

function isAlive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

function signal(pid: number, sig: NodeJS.Signals): void {
  if (pid === process.pid) return;
  try {
    process.kill(pid, sig);
  } catch {
    /* already gone */
  }
}

const sleep = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));

/** The outcome of terminating a stage's process tree. */
export interface StageTreeTermination {
  /** Every pid the tree held, root included. */
  pids: number[];
  /** Pids still alive after SIGKILL and the verification window. */
  survivors: number[];
}

/**
 * Terminate a stage's whole process tree: SIGTERM every member, SIGKILL
 * whatever is left once `graceMs` has passed (or at once when the root exits
 * first), then verify, for up to `verifyMs`, that no member survives. The
 * tree is re-read before SIGKILL, so a child spawned while the stage handled
 * SIGTERM is killed too. `isRootGone` lets the caller say its child has been
 * reaped: an unreaped child still answers signal 0.
 */
export async function terminateStageProcessTree(
  rootPid: number,
  options: {
    graceMs: number;
    verifyMs?: number;
    isRootGone?: () => boolean;
    /** Signals the root; the caller's ChildProcess.kill. */
    signalRoot?: (sig: NodeJS.Signals) => void;
    log?: (line: string) => void;
  }
): Promise<StageTreeTermination> {
  const send = (pid: number, sig: NodeJS.Signals) => {
    if (pid === rootPid && options.signalRoot) {
      try {
        options.signalRoot(sig);
      } catch {
        /* already gone */
      }
      return;
    }
    signal(pid, sig);
  };
  const known = new Set<number>(await listProcessTree(rootPid));
  known.add(rootPid);
  for (const pid of known) send(pid, "SIGTERM");

  const rootGone = () => options.isRootGone?.() ?? !isAlive(rootPid);
  const deadline = Date.now() + options.graceMs;
  while (Date.now() < deadline && !rootGone()) await sleep(50);

  if (!rootGone()) {
    for (const pid of await listProcessTree(rootPid)) known.add(pid);
  }
  for (const pid of known) {
    if (pid === rootPid ? !rootGone() : isAlive(pid)) send(pid, "SIGKILL");
  }

  const alive = (pid: number) => (pid === rootPid ? !rootGone() : isAlive(pid));
  const verifyUntil = Date.now() + (options.verifyMs ?? 2000);
  let survivors = [...known].filter(alive);
  while (survivors.length > 0 && Date.now() < verifyUntil) {
    for (const pid of survivors) send(pid, "SIGKILL");
    await sleep(50);
    survivors = [...known].filter(alive);
  }
  if (survivors.length > 0) {
    options.log?.(
      `[stage-budget] ${survivors.length} member(s) of the stage's process tree survived SIGKILL: ${survivors.join(",")}\n`
    );
  }
  return { pids: [...known], survivors };
}

/**
 * The merge-queue wait the binary has recorded for `issue` since `sinceMs`
 * (`nightgauge pr-stage queue-wait`, #2770), in milliseconds. The exclusion
 * arithmetic lives in the binary; this only asks. Asynchronous, because the
 * extension host must never block on a subprocess (#2884).
 */
export function queueWaitExcludedMs(
  binary: string,
  workspaceRoot: string,
  issue: number,
  sinceMs: number,
  run: ExecFileRunner = defaultExecFile
): Promise<number> {
  const args = [
    "pr-stage",
    "queue-wait",
    String(issue),
    "--since-ms",
    String(Math.floor(sinceMs)),
    "--workdir",
    workspaceRoot,
    "--json",
  ];
  return run(binary, args).then((stdout) => {
    const parsed = JSON.parse(stdout) as { excluded_ms?: unknown };
    return nonNegative(parsed.excluded_ms);
  });
}

/** Runs a binary and resolves with its stdout. */
export type ExecFileRunner = (binary: string, args: string[]) => Promise<string>;

const QUEUE_WAIT_LOOKUP_TIMEOUT_MS = 10_000;

function defaultExecFile(binary: string, args: string[]): Promise<string> {
  return new Promise((resolve, reject) => {
    execFile(
      binary,
      args,
      { encoding: "utf-8", timeout: QUEUE_WAIT_LOOKUP_TIMEOUT_MS },
      (err, stdout) => (err ? reject(err) : resolve(stdout))
    );
  });
}
