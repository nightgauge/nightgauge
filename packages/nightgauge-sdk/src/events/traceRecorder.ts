/**
 * TraceRecorder — the SDK ("sdk" producer) writer for the per-run lifecycle
 * decision trace (ADR 013, docs/decisions/013-run-lifecycle-trace-schema.md).
 *
 * Appends events to the same per-run JSONL the Go binary writes
 * (`.nightgauge/pipeline/trace/<run_id>.jsonl`), under the same envelope:
 * `seq` is monotonic per (run_id, producer) and total order is
 * (ts, producer, seq), so the two producers interleave without coordination.
 *
 * Fail-open by contract: recorder errors never fail a stage or the run.
 * Every write is queued on a serialized promise chain so `seq` assignment and
 * append order can never interleave, mirroring the WorkflowExecutor journal
 * pattern.
 *
 * @see Issue #180 — persist phase + orchestrator decision events (Wave 2 of #178)
 */

import * as fs from "node:fs/promises";
import * as path from "node:path";
import { RunStateManager } from "../context/RunStateManager.js";

/** Envelope version — must match the Go writer (`internal/trace.SchemaVersion`). */
export const TRACE_SCHEMA_VERSION = 1;

/** Producer discriminator for events written by this recorder. */
export const TRACE_PRODUCER_SDK = "sdk";

/**
 * Closed kind taxonomy from ADR 013. Mirrors the Go `trace.Kind` constants —
 * keep in sync (the ADR is the contract).
 */
export type TraceEventKind =
  | "stage_start"
  | "stage_exit"
  | "phase_transition"
  | "model_routing"
  | "change_class"
  | "stage_skip"
  | "complexity_escalation"
  | "backtrack"
  | "recovery_retry"
  | "gate_result"
  | "outcome";

/** One trace event line — wire shape is snake_case per ADR 013. */
export interface TraceEvent {
  schema_version: number;
  run_id: string;
  repo?: string;
  issue?: number;
  seq: number;
  ts: string;
  stage?: string;
  phase?: string;
  kind: TraceEventKind;
  producer: string;
  payload?: Record<string, unknown>;
}

/**
 * Run ids must be filesystem-safe before they become a filename. Mirrors the
 * Go writer's guard (`internal/trace/store.go` runIDPattern).
 */
const RUN_ID_PATTERN = /^[A-Za-z0-9_-]{8,128}$/;

export interface TraceRecorderOptions {
  /**
   * The pipeline context directory (normally `<root>/.nightgauge/pipeline`).
   * `run-state.json` is read from here and the trace is written to its
   * `trace/` subdirectory — one knob so custom `contextPath` configurations
   * keep run-state and trace co-located.
   */
  pipelineDir: string;
  /**
   * Explicit run id. When omitted, the recorder resolves `run_id` from
   * `.nightgauge/pipeline/run-state.json` and disables itself (silent no-op)
   * when no run-state exists — a per-stage caller must never invent a run id
   * or it would split one run's trace across files.
   */
  runId?: string;
  /** "owner/name" of the repository the run executes against. */
  repo?: string;
  /** GitHub issue number the run is dispatched for. */
  issue?: number;
}

/**
 * Serialized, fail-open trace writer for one run.
 *
 * Construction is synchronous; run-id resolution, seq seeding from an
 * existing file (crash resume / Go-writer interleave), and every append are
 * links on one internal promise chain, so callers can emit fire-and-forget
 * from synchronous code. Await {@link flush} to drain (tests, process exit).
 */
export class TraceRecorder {
  private readonly pipelineDir: string;
  private readonly repo?: string;
  private readonly issue?: number;

  private runId: string | null = null;
  private filePath: string | null = null;
  private disabled = false;
  private seq = 0;
  private warned = false;

  /** Serialized operation chain — keeps seq assignment + append atomic. */
  private pending: Promise<void>;

  /** Last phase seen per stage, for per-phase duration computation. */
  private readonly lastPhase = new Map<string, { name: string; atMs: number }>();

  private constructor(opts: TraceRecorderOptions) {
    this.pipelineDir = opts.pipelineDir;
    this.repo = opts.repo;
    this.issue = opts.issue;
    this.pending = this.init(opts.runId);
  }

  /**
   * Open a recorder for a run. Never throws; a recorder that cannot resolve a
   * usable run id becomes a silent no-op.
   */
  static open(opts: TraceRecorderOptions): TraceRecorder {
    return new TraceRecorder(opts);
  }

  /** The resolved run id, once init has completed (null when disabled). */
  getRunId(): string | null {
    return this.runId;
  }

  /** True when the recorder resolved a run id and can append. */
  isEnabled(): boolean {
    return !this.disabled && this.filePath !== null;
  }

  private async init(explicitRunId?: string): Promise<void> {
    try {
      let runId = explicitRunId ?? null;
      if (!runId) {
        const state = await new RunStateManager(this.pipelineDir).read();
        runId = state?.run_id ?? null;
      }
      if (!runId && this.issue !== undefined) {
        // Interactive (HeadlessOrchestrator) path: nothing writes run_id into
        // run-state.json today, but the Go IPC notify handler persists the
        // platform-facing RunID to runtime-{issue}.json. Resolve it so the SDK
        // trace shares the run_id the platform materialises its pipeline_runs
        // row from — making the trace joinable in the Lifecycle Explorer and
        // discoverable by `nightgauge trace show <issue>`. (#228)
        runId = await this.readRuntimeRunId(this.issue);
      }
      if (!runId || !RUN_ID_PATTERN.test(runId)) {
        this.disabled = true;
        // #228: make the disable path observable instead of a silent no-op.
        // Fires at most once per recorder; fail-open (never throws).
        if (!this.warned) {
          this.warned = true;
          console.debug(
            "[traceRecorder] disabled: no usable run_id (explicit runId and " +
              "run-state.json run_id both absent/invalid) — phase trace will not be written"
          );
        }
        return;
      }
      this.runId = runId;
      const dir = path.join(this.pipelineDir, "trace");
      this.filePath = path.join(dir, `${runId}.jsonl`);
      await fs.mkdir(dir, { recursive: true });

      // Seed seq from the existing file's line count so a restarted recorder
      // (or one joining a file the Go writer already appended to) stays
      // monotonic. Over-counting is legal per ADR 013 — gaps are allowed,
      // regressions are not.
      try {
        const content = await fs.readFile(this.filePath, "utf-8");
        this.seq = content.split("\n").filter((l) => l.length > 0).length;
      } catch {
        this.seq = 0;
      }
    } catch (err) {
      this.disabled = true;
      this.warnOnce(err);
    }
  }

  /**
   * Append one event. Fire-and-forget: ordering is preserved by the internal
   * chain and failures are swallowed (fail-open).
   */
  emit(
    kind: TraceEventKind,
    fields?: { stage?: string; phase?: string; payload?: Record<string, unknown> }
  ): void {
    this.pending = this.pending.then(async () => {
      if (this.disabled || !this.filePath || !this.runId) {
        return;
      }
      this.seq += 1;
      const event: TraceEvent = {
        schema_version: TRACE_SCHEMA_VERSION,
        run_id: this.runId,
        ...(this.repo ? { repo: this.repo } : {}),
        ...(this.issue && this.issue > 0 ? { issue: this.issue } : {}),
        seq: this.seq,
        ts: new Date().toISOString(),
        ...(fields?.stage ? { stage: fields.stage } : {}),
        ...(fields?.phase ? { phase: fields.phase } : {}),
        kind,
        producer: TRACE_PRODUCER_SDK,
        ...(fields?.payload ? { payload: fields.payload } : {}),
      };
      try {
        await fs.appendFile(this.filePath, `${JSON.stringify(event)}\n`, "utf-8");
      } catch (err) {
        this.warnOnce(err);
      }
    });
  }

  /**
   * Record a phase-marker transition for a stage, carrying the completed
   * previous phase's duration so replay shows where time went inside the
   * stage.
   */
  phaseTransition(stage: string, marker: { name: string; index: number; total: number }): void {
    const nowMs = Date.now();
    const prev = this.lastPhase.get(stage);
    this.lastPhase.set(stage, { name: marker.name, atMs: nowMs });

    // Re-announcements of the phase we are already in carry no transition
    // information — skip them so durations stay per-phase, not per-marker.
    if (prev && prev.name === marker.name) {
      return;
    }

    const payload: Record<string, unknown> = {
      index: marker.index,
      total: marker.total,
    };
    if (prev) {
      payload["prev_phase"] = prev.name;
      payload["prev_phase_duration_ms"] = Math.max(0, nowMs - prev.atMs);
    }
    this.emit("phase_transition", { stage, phase: marker.name, payload });
  }

  /** Record a backtrack decision with its rationale and evidence (ADR 013). */
  backtrack(fields: {
    fromStage: string;
    targetStage: string;
    signalType: string;
    rationale: string;
    evidence?: string[];
    trigger: string;
  }): void {
    this.emit("backtrack", {
      stage: fields.fromStage,
      payload: {
        from_stage: fields.fromStage,
        target_stage: fields.targetStage,
        signal_type: fields.signalType,
        rationale: fields.rationale,
        ...(fields.evidence && fields.evidence.length > 0 ? { evidence: fields.evidence } : {}),
        trigger: fields.trigger,
      },
    });
  }

  /** Record a stage-skip execution decision. */
  stageSkip(stage: string, source: string, reason: string): void {
    this.emit("stage_skip", { stage, payload: { source, reason } });
  }

  /** Drain the operation chain — for tests and orderly shutdown. */
  async flush(): Promise<void> {
    await this.pending;
  }

  /**
   * Best-effort read of the platform-facing RunID the Go IPC notify handler
   * persists to `runtime-{issue}.json`. Fail-open: any error (missing file,
   * malformed JSON, no runId) yields null so the recorder falls through to its
   * disabled no-op rather than throwing. (#228)
   */
  private async readRuntimeRunId(issue: number): Promise<string | null> {
    try {
      const file = path.join(this.pipelineDir, `runtime-${issue}.json`);
      const raw = await fs.readFile(file, "utf-8");
      const parsed = JSON.parse(raw) as { runId?: unknown };
      return typeof parsed.runId === "string" && parsed.runId.length > 0 ? parsed.runId : null;
    } catch {
      return null;
    }
  }

  private warnOnce(err: unknown): void {
    if (this.warned) {
      return;
    }
    this.warned = true;
    const msg = err instanceof Error ? err.message : String(err);

    console.warn(`[traceRecorder] trace capture disabled/degraded (fail-open): ${msg}`);
  }
}
